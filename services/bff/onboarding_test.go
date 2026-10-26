package bff_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	kycsim "github.com/aibank/aibank/adapters/kyc/sim"
	"github.com/aibank/aibank/clients/go/coreclient"
	"github.com/aibank/aibank/services/bff"
	"github.com/aibank/aibank/services/onboarding"
	"github.com/google/uuid"
)

// El alta es la única puerta del canal sin sesión. Lo que se prueba es que sea
// estrecha: que no abra dos cuentas por un reintento, que no le pague dos veces
// al proveedor, que no le diga a nadie por qué se le revisa, y que no se pueda
// martillear.

// Fecha fija para que la edad no dependa del día en que corre la suite.
var today = time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)

type onboardingEnv struct {
	srv      *httptest.Server
	core     *coreclient.Client
	verifier *kycsim.IdentityVerifier
	screener *kycsim.SanctionsScreener
	product  string
}

func newOnboardingEnv(t *testing.T, opts ...bff.OnboardingOption) *onboardingEnv {
	t.Helper()
	f := setup(t)

	db, err := sql.Open("postgres", bffDatabaseURL())
	if err != nil {
		t.Fatalf("abrir base: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := onboarding.NewStore(db)
	if err := store.Migrate(f.ctx); err != nil {
		t.Skipf("base no disponible: %v", err)
	}

	product := "BASICA-" + uuid.NewString()[:8]
	maxBalance, maxTx := int64(1_000_000000), int64(300_000000)
	if _, err := f.core.CreateProduct(f.ctx, coreclient.NewProduct{
		Code: product, Name: "Básica", Currency: "USD",
		MaxBalanceMicros: &maxBalance, MaxTransactionMicros: &maxTx,
	}); err != nil {
		t.Fatalf("producto: %v", err)
	}

	verifier := kycsim.NewIdentityVerifier()
	screener := kycsim.NewSanctionsScreener()
	svc := onboarding.NewService(store, f.core, verifier, screener)

	opts = append([]bff.OnboardingOption{bff.WithOnboardingClock(func() time.Time { return today })}, opts...)
	server := bff.NewServer(f.core, f.auth, nil).WithOnboarding(svc, store, product, opts...)
	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)

	return &onboardingEnv{srv: ts, core: f.core, verifier: verifier, screener: screener, product: product}
}

// dni devuelve un DNI que no usó ningún otro test: la base persiste, y el alta
// manda a revisión a quien ya tiene otra alta viva.
func dni() string { return fmt.Sprintf("%08d", rand.Intn(100_000_000)) }

func applicant(document string) map[string]any {
	return map[string]any{
		"reference":       uuid.NewString(),
		"document_number": document,
		"first_names":     "Ana María",
		"last_names":      "Pérez Quispe",
		"date_of_birth":   "1995-03-14",
	}
}

func (e *onboardingEnv) submit(t *testing.T, body map[string]any) (int, map[string]any, string) {
	t.Helper()
	raw, _ := json.Marshal(body)
	resp, err := http.Post(e.srv.URL+"/v1/onboarding", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("alta: %v", err)
	}
	defer resp.Body.Close()
	text, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(text, &out)
	return resp.StatusCode, out, string(text)
}

func TestUnAltaAprobadaAbreUnaCuentaDeNivelUno(t *testing.T) {
	e := newOnboardingEnv(t)

	status, out, body := e.submit(t, applicant(dni()))
	if status != http.StatusOK || out["status"] != "approved" {
		t.Fatalf("status = %d: %s", status, body)
	}

	account, err := e.core.GetAccountByID(t.Context(), out["account_id"].(string))
	if err != nil {
		t.Fatalf("la cuenta no existe en el core: %v", err)
	}
	product, _ := e.core.GetProduct(t.Context(), e.product)
	// Nivel 1: el producto de topes bajos, no uno cualquiera.
	if account.ProductId != product.Id {
		t.Errorf("producto = %s, se esperaba el de nivel 1", account.ProductId)
	}
	if account.OwnerId != out["customer_id"] {
		t.Errorf("la cuenta es de %s, no del cliente devuelto %v", account.OwnerId, out["customer_id"])
	}
}

// El corte de red más común: la respuesta no llega y la app reenvía. Con la
// misma referencia, es la misma alta.
func TestReenviarLaMismaAltaNoAbreOtraCuentaNiPagaOtraVerificacion(t *testing.T) {
	e := newOnboardingEnv(t)
	req := applicant(dni())

	_, first, _ := e.submit(t, req)
	_, second, _ := e.submit(t, req)

	if first["account_id"] != second["account_id"] {
		t.Errorf("dos cuentas: %v y %v", first["account_id"], second["account_id"])
	}
	if e.verifier.Calls() != 1 {
		t.Errorf("verificaciones pagadas = %d, se esperaba 1", e.verifier.Calls())
	}
}

// Reanudar el expediente de otra persona abriría su cuenta con datos ajenos.
func TestUnaReferenciaNoSeReutilizaConOtraPersona(t *testing.T) {
	e := newOnboardingEnv(t)
	req := applicant(dni())
	e.submit(t, req)

	otra := applicant(dni())
	otra["reference"] = req["reference"]
	status, out, _ := e.submit(t, otra)
	if status != http.StatusConflict || out["code"] != "reference_conflict" {
		t.Errorf("status = %d, code = %v; se esperaba 409 reference_conflict", status, out["code"])
	}
}

// La normativa contra el lavado prohíbe avisar a alguien de que coincide con
// una lista de sanciones. La respuesta tiene que ser indistinguible de la de
// una foto borrosa.
func TestUnaCoincidenciaEnListasNoSeLeRevelaAlInteresado(t *testing.T) {
	e := newOnboardingEnv(t)

	enLista := dni()
	e.screener.AddToWatchlist(enLista, "OFAC-SDN")
	borrosa := dni()
	e.verifier.SetOutcome(borrosa, kycsim.Inconclusive("imagen ilegible"))

	_, a, bodyA := e.submit(t, applicant(enLista))
	_, b, _ := e.submit(t, applicant(borrosa))

	if a["status"] != "in_review" || b["status"] != "in_review" {
		t.Fatalf("estados = %v y %v; se esperaba in_review en los dos", a["status"], b["status"])
	}
	lower := strings.ToLower(bodyA)
	for _, prohibido := range []string{"ofac", "lista", "sanci", "coincid", "reason", "motivo"} {
		if strings.Contains(lower, prohibido) {
			t.Errorf("la respuesta revela %q: %s", prohibido, bodyA)
		}
	}
	// Mismas claves en las dos respuestas: ni la forma del JSON distingue los casos.
	if len(a) != len(b) {
		t.Errorf("las respuestas tienen forma distinta: %v vs %v", a, b)
	}
}

func TestUnaIdentidadRechazadaNoAbreCuenta(t *testing.T) {
	e := newOnboardingEnv(t)
	doc := dni()
	e.verifier.SetOutcome(doc, kycsim.Rejected("prueba de vida no superada"))

	_, out, _ := e.submit(t, applicant(doc))
	if out["status"] != "rejected" || out["account_id"] != nil {
		t.Errorf("respuesta = %v", out)
	}
}

// Si el proveedor no responde, el alta queda donde estaba. Reenviar con la
// misma referencia la retoma sin volver a pagar lo ya hecho.
func TestSiElProveedorFallaSeReintentaSinPagarDosVeces(t *testing.T) {
	e := newOnboardingEnv(t)
	e.verifier.FailNext(1)
	req := applicant(dni())

	status, out, _ := e.submit(t, req)
	if status != http.StatusServiceUnavailable || out["code"] != "unavailable" {
		t.Fatalf("status = %d, code = %v; se esperaba 503", status, out["code"])
	}

	_, out, _ = e.submit(t, req)
	if out["status"] != "approved" {
		t.Fatalf("reintento = %v", out)
	}
	if e.verifier.Calls() != 1 {
		t.Errorf("verificaciones pagadas = %d, se esperaba 1", e.verifier.Calls())
	}
}

func TestLaValidacionDiceQueCampoFalla(t *testing.T) {
	// Límite holgado: son seis peticiones seguidas desde la misma IP, y con el
	// de serie la sexta rebotaría por tasa antes de llegar a validarse.
	e := newOnboardingEnv(t, bff.WithOnboardingLimit(100, 100))

	casos := []struct {
		nombre string
		campo  string
		valor  any
		code   string
	}{
		{"DNI corto", "document_number", "1234567", "invalid_document"},
		{"DNI con letras", "document_number", "1234567A", "invalid_document"},
		{"sin apellidos", "last_names", "   ", "invalid_name"},
		{"fecha imposible", "date_of_birth", "1995-02-30", "invalid_date"},
		{"menor de edad", "date_of_birth", "2010-01-01", "underage"},
		{"referencia inventada", "reference", "mi-referencia", "invalid_reference"},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			req := applicant(dni())
			req[c.campo] = c.valor
			status, out, _ := e.submit(t, req)
			if status != http.StatusBadRequest || out["code"] != c.code {
				t.Errorf("status = %d, code = %v; se esperaba 400 %s", status, out["code"], c.code)
			}
		})
	}
}

// La frontera exacta: cumple 18 hoy, puede; los cumple mañana, no.
func TestLaMayoriaDeEdadSeCuentaAlDia(t *testing.T) {
	e := newOnboardingEnv(t)

	cumpleHoy := applicant(dni())
	cumpleHoy["date_of_birth"] = "2008-10-25"
	if _, out, _ := e.submit(t, cumpleHoy); out["status"] != "approved" {
		t.Errorf("quien cumple 18 hoy: %v", out)
	}

	cumpleManana := applicant(dni())
	cumpleManana["date_of_birth"] = "2008-10-26"
	if _, out, _ := e.submit(t, cumpleManana); out["code"] != "underage" {
		t.Errorf("quien cumple 18 mañana: %v", out)
	}
}

// Cada alta encarga verificaciones que se pagan. Sin límite, cualquiera podría
// encargarlas en bucle y pasarnos la factura.
func TestElAltaTieneLimiteDeTasa(t *testing.T) {
	e := newOnboardingEnv(t, bff.WithOnboardingLimit(1, 1))

	e.submit(t, applicant(dni()))
	raw, _ := json.Marshal(applicant(dni()))
	resp, err := http.Post(e.srv.URL+"/v1/onboarding", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") == "" {
		t.Errorf("status = %d, Retry-After = %q; se esperaba 429 con Retry-After",
			resp.StatusCode, resp.Header.Get("Retry-After"))
	}
}

func TestElEstadoSoloSeConsultaConLaReferencia(t *testing.T) {
	e := newOnboardingEnv(t)
	req := applicant(dni())
	_, submitted, _ := e.submit(t, req)

	get := func(ref string) (int, map[string]any) {
		r, _ := http.NewRequest(http.MethodGet, e.srv.URL+"/v1/onboarding", nil)
		if ref != "" {
			r.Header.Set("X-Onboarding-Reference", ref)
		}
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}

	if status, _ := get(""); status != http.StatusBadRequest {
		t.Errorf("sin referencia = %d, se esperaba 400", status)
	}
	if status, _ := get(uuid.NewString()); status != http.StatusNotFound {
		t.Errorf("referencia desconocida = %d, se esperaba 404", status)
	}
	status, out := get(req["reference"].(string))
	if status != http.StatusOK || out["account_id"] != submitted["account_id"] {
		t.Errorf("consulta = %d %v", status, out)
	}
}
