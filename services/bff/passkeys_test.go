package bff_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	kycsim "github.com/aibank/aibank/adapters/kyc/sim"
	"github.com/aibank/aibank/clients/go/coreclient"
	corev1 "github.com/aibank/aibank/clients/go/corev1"
	"github.com/aibank/aibank/services/agents"
	"github.com/aibank/aibank/services/aliases"
	"github.com/aibank/aibank/services/bff"
	"github.com/aibank/aibank/services/control"
	"github.com/aibank/aibank/services/credit"
	"github.com/aibank/aibank/services/devices"
	"github.com/aibank/aibank/services/forecast"
	"github.com/aibank/aibank/services/oauth"
	"github.com/aibank/aibank/services/onboarding"
	"github.com/aibank/aibank/services/passkeys"
	"github.com/aibank/aibank/services/sessions"
	"github.com/aibank/aibank/services/stepup"
	"github.com/descope/virtualwebauthn"
	"github.com/google/uuid"
)

// El recorrido completo de una persona real, por HTTP, contra un core real y un
// autenticador que firma de verdad: se da de alta, protege su cuenta con una
// passkey, sale, y vuelve a entrar sin escribir nada.

const passkeyRPID = "aerarium.test"

var passkeyRP = virtualwebauthn.RelyingParty{ID: passkeyRPID, Name: "AIBank", Origin: "https://" + passkeyRPID}

type passkeyEnv struct {
	srv *httptest.Server
	f   *fixture
	db  *sql.DB
}

func newPasskeyEnv(t *testing.T) *passkeyEnv {
	t.Helper()
	f := setup(t)

	db, err := sql.Open("postgres", bffDatabaseURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	onb := onboarding.NewStore(db)
	if err := onb.Migrate(f.ctx); err != nil {
		t.Skipf("base no disponible: %v", err)
	}
	pkStore := passkeys.NewStore(db)
	sess := sessions.NewStore(db)
	devStore := devices.NewStore(db)
	for _, m := range []interface{ Migrate(context.Context) error }{pkStore, sess, devStore} {
		if err := m.Migrate(f.ctx); err != nil {
			t.Fatal(err)
		}
	}

	product := "BASICA-" + uuid.NewString()[:8]
	// Con los topes de la cuenta de nivel 1, como la del alta real: 300 por
	// operación, 1.000 de saldo. Sin ellos, el saldo de prueba pasaba aquí y
	// fallaba en el teléfono.
	maxBalance, maxTx := int64(1_000_000_000), int64(300_000_000)
	if _, err := f.core.CreateProduct(f.ctx, coreclient.NewProduct{
		Code: product, Name: "Básica", Currency: "USD",
		MaxBalanceMicros: &maxBalance, MaxTransactionMicros: &maxTx,
	}); err != nil {
		t.Fatal(err)
	}
	onboardingSvc := onboarding.NewService(onb, f.core, kycsim.NewIdentityVerifier(), kycsim.NewSanctionsScreener())

	pk, err := passkeys.NewService(passkeys.Config{
		RPID: passkeyRPID, RPDisplayName: "AIBank", RPOrigins: []string{"https://" + passkeyRPID},
	}, pkStore, onb, sess)
	if err != nil {
		t.Fatal(err)
	}

	payees := stepup.NewService(db, stepup.DefaultPolicy())
	if err := payees.Migrate(f.ctx); err != nil {
		t.Fatal(err)
	}
	ctrl := control.NewService(db, payees)
	if err := ctrl.Migrate(f.ctx); err != nil {
		t.Fatal(err)
	}
	// La caja del saldo de prueba: el prefijo más la moneda es su código.
	cashPrefix := "TESTCASH-" + uuid.NewString()[:8] + "-"
	if _, err := f.core.CreateInternalAccount(f.ctx, cashPrefix+"USD", "Caja de prueba",
		corev1.AccountType_ACCOUNT_TYPE_ASSET, "USD"); err != nil {
		t.Fatal(err)
	}

	deviceSvc := devices.NewService(devices.Config{}, devStore, onb, sess)
	// Un motor de riesgo de mentira que aprueba 500, y que espera un sueldo de
	// 1.500 dentro de 25 días.
	risk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/income" {
			_, _ = w.Write([]byte(`{"recurring_income":{"counterparty":"empleador","typical_amount_micros":1500000000,` +
				`"occurrences":6,"cadence":"monthly","last_received_on":"2026-01-01","next_expected_on":"` +
				time.Now().AddDate(0, 0, 25).Format(time.DateOnly) + `"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"outcome":"approved","limit_micros":500000000,"currency":"USD","score":700,` +
			`"model_version":"scorecard-v1","reasons":[]}`))
	}))
	t.Cleanup(risk.Close)
	assessor := credit.NewHTTPAssessor(risk.URL)
	cards := credit.NewService(db, f.core, assessor, credit.Config{})
	if err := cards.Migrate(f.ctx); err != nil {
		t.Fatal(err)
	}
	sender := aliases.NewSandboxSender()
	directory := aliases.NewService(db, sender)
	if err := directory.Migrate(f.ctx); err != nil {
		t.Fatal(err)
	}
	// La plataforma de prueba que pide permisos desde el sandbox. Puede existir
	// ya, de una corrida anterior.
	consents := oauth.NewStore(db)
	if err := consents.Migrate(f.ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := consents.FindClient(f.ctx, bff.SandboxPlatform); err != nil {
		if _, err := consents.Register(f.ctx, bff.SandboxPlatform, "Mercatus", []string{oauth.ScopePaymentsWrite}); err != nil {
			// Otra prueba en paralelo la registró primero.
			if _, findErr := consents.FindClient(f.ctx, bff.SandboxPlatform); findErr != nil {
				t.Fatal(err)
			}
		}
	}
	agentStore := agents.NewStore(db)
	if err := agentStore.Migrate(f.ctx); err != nil {
		t.Fatal(err)
	}
	auth := bff.ChainAuthenticator{bff.NewSessionAuthenticator(sess)}
	server := bff.NewServer(f.core, auth, nil).
		WithOnboarding(onboardingSvc, onb, product, bff.WithOnboardingLimit(100, 100)).
		WithSessions(sess).
		WithPasskeys(pk).
		WithDevices(deviceSvc, bff.WithDeviceLimit(100, 100)).
		WithControl(ctrl, payees, deviceSvc).
		WithSandboxFunds(cashPrefix, "RETAIL-").
		WithAliases(directory, sender, bff.WithLookupLimit(100, 100)).
		WithCredit(cards).
		WithForecast(forecast.NewService(f.core, cards, assessor, time.UTC)).
		WithConsents(consents).
		WithAgents(agentStore)
	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)
	return &passkeyEnv{srv: ts, f: f, db: db}
}

func (e *passkeyEnv) call(t *testing.T, method, path, token string, body any) (int, map[string]any) {
	t.Helper()
	return e.callWith(t, method, path, token, nil, body)
}

func (e *passkeyEnv) callWith(t *testing.T, method, path, token string, headers map[string]string, body any) (int, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// phone es un teléfono con su autenticador de plataforma.
type phone struct {
	auth virtualwebauthn.Authenticator
	cred virtualwebauthn.Credential
	rp   virtualwebauthn.RelyingParty
}

func newPhone(customerID string) *phone {
	id := uuid.MustParse(customerID)
	return &phone{
		auth: virtualwebauthn.NewAuthenticatorWithOptions(virtualwebauthn.AuthenticatorOptions{UserHandle: id[:]}),
		cred: virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2),
		rp:   passkeyRP,
	}
}

func (p *phone) create(t *testing.T, options any) json.RawMessage {
	t.Helper()
	raw, _ := json.Marshal(options)
	parsed, err := virtualwebauthn.ParseAttestationOptions(string(raw))
	if err != nil {
		t.Fatalf("opciones de registro: %v", err)
	}
	return json.RawMessage(virtualwebauthn.CreateAttestationResponse(p.rp, p.auth, p.cred, *parsed))
}

func (p *phone) get(t *testing.T, options any) json.RawMessage {
	t.Helper()
	raw, _ := json.Marshal(options)
	parsed, err := virtualwebauthn.ParseAssertionOptions(string(raw))
	if err != nil {
		t.Fatalf("opciones de login: %v", err)
	}
	p.auth.AddCredential(p.cred)
	p.cred.Counter++
	return json.RawMessage(virtualwebauthn.CreateAssertionResponse(p.rp, p.auth, p.cred, *parsed))
}

// approve da de alta a una persona por HTTP y devuelve la referencia del alta.
func (e *passkeyEnv) approve(t *testing.T) (reference, customer, account string) {
	t.Helper()
	reference = uuid.NewString()
	req := applicant(dni())
	req["reference"] = reference
	status, alta := e.call(t, http.MethodPost, "/v1/onboarding", "", req)
	if status != http.StatusOK || alta["status"] != "approved" {
		t.Fatalf("alta: %d %v", status, alta)
	}
	return reference, alta["customer_id"].(string), alta["account_id"].(string)
}

// onboard da de alta a una persona y registra la passkey de su teléfono.
func (e *passkeyEnv) onboard(t *testing.T) (token, customer, account string, device *phone) {
	t.Helper()
	reference, customer, account := e.approve(t)

	status, begin := e.call(t, http.MethodPost, "/v1/passkeys/enrollment", "", map[string]any{"reference": reference})
	if status != http.StatusOK {
		t.Fatalf("empezar registro: %d %v", status, begin)
	}
	device = newPhone(customer)
	status, session := e.call(t, http.MethodPost, "/v1/passkeys/registration", "", map[string]any{
		"ceremony_id": begin["ceremony_id"], "credential": device.create(t, begin["options"]),
	})
	if status != http.StatusCreated {
		t.Fatalf("terminar registro: %d %v", status, session)
	}
	return session["token"].(string), customer, account, device
}

func (e *passkeyEnv) login(t *testing.T, device *phone) (int, map[string]any) {
	t.Helper()
	_, begin := e.call(t, http.MethodPost, "/v1/passkeys/login/options", "", map[string]any{})
	return e.call(t, http.MethodPost, "/v1/passkeys/login", "", map[string]any{
		"ceremony_id": begin["ceremony_id"], "credential": device.get(t, begin["options"]),
	})
}

func TestDelAltaALaCuentaProtegidaConPasskey(t *testing.T) {
	e := newPasskeyEnv(t)
	token, _, account, _ := e.onboard(t)

	// Tras entrar, la app no sabe qué cuenta mostrar: la pide.
	status, accounts := e.call(t, http.MethodGet, "/v1/accounts", token, nil)
	if status != http.StatusOK {
		t.Fatalf("cuentas: %d", status)
	}
	list := accounts["accounts"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["id"] != account {
		t.Fatalf("cuentas = %v, se esperaba la abierta en el alta (%s)", list, account)
	}

	if status, _ := e.call(t, http.MethodGet, "/v1/home?account_id="+account, token, nil); status != http.StatusOK {
		t.Errorf("inicio con la sesión de la passkey: %d", status)
	}
}

func TestSalirYVolverAEntrarConLaPasskey(t *testing.T) {
	e := newPasskeyEnv(t)
	token, customer, account, device := e.onboard(t)

	if status, _ := e.call(t, http.MethodPost, "/v1/session/logout", token, nil); status != http.StatusNoContent {
		t.Fatalf("salir: %d", status)
	}
	if status, _ := e.call(t, http.MethodGet, "/v1/accounts", token, nil); status != http.StatusUnauthorized {
		t.Errorf("la sesión cerrada sigue sirviendo: %d", status)
	}

	// Volver a entrar: sin usuario, sin contraseña, sin OTP.
	status, session := e.login(t, device)
	if status != http.StatusOK || session["customer_id"] != customer {
		t.Fatalf("login: %d %v", status, session)
	}
	if status, _ := e.call(t, http.MethodGet, "/v1/home?account_id="+account, session["token"].(string), nil); status != http.StatusOK {
		t.Errorf("inicio tras volver a entrar: %d", status)
	}
}

// Una passkey que el banco no conoce y una firma desde un dominio de phishing
// reciben la misma respuesta: distinguirlas le diría a quien prueba qué existe.
func TestUnaPasskeyRechazadaNoDiceSiExiste(t *testing.T) {
	e := newPasskeyEnv(t)
	_, _, _, device := e.onboard(t)

	phishing := *device
	phishing.rp.Origin = "https://aerarium-seguro.test"
	statusA, a := e.login(t, &phishing)

	stranger := newPhone(uuid.NewString())
	statusB, b := e.login(t, stranger)

	if statusA != http.StatusUnauthorized || statusB != http.StatusUnauthorized {
		t.Fatalf("estados = %d y %d; se esperaba 401 en los dos", statusA, statusB)
	}
	if a["code"] != "passkey_rejected" || a["code"] != b["code"] || a["message"] != b["message"] {
		t.Errorf("las respuestas se distinguen: %v vs %v", a, b)
	}
}

func TestUnaReferenciaAjenaNoRegistraPasskeys(t *testing.T) {
	e := newPasskeyEnv(t)
	status, out := e.call(t, http.MethodPost, "/v1/passkeys/enrollment", "", map[string]any{"reference": uuid.NewString()})
	if status != http.StatusForbidden || out["code"] != "not_enrollable" {
		t.Errorf("referencia inventada: %d %v", status, out)
	}
}

// Sin sesión no se cuelga un segundo dispositivo de la cuenta de nadie.
func TestAnadirUnDispositivoExigeSesion(t *testing.T) {
	e := newPasskeyEnv(t)
	if status, _ := e.call(t, http.MethodPost, "/v1/passkeys/devices", "", nil); status != http.StatusUnauthorized {
		t.Errorf("sin sesión: %d, se esperaba 401", status)
	}
	token, _, _, _ := e.onboard(t)
	if status, _ := e.call(t, http.MethodPost, "/v1/passkeys/devices", token, nil); status != http.StatusOK {
		t.Errorf("con sesión: %d, se esperaba 200", status)
	}
}
