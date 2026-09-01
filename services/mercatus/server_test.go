package mercatus

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aibank/aibank/clients/go/coreclient"
	corev1 "github.com/aibank/aibank/clients/go/corev1"
	"github.com/aibank/aibank/services/oauth"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
)

func databaseURL() string {
	if u := os.Getenv("DATABASE_URL"); u != "" {
		return u
	}
	return "postgres://aibank:aibank_dev@localhost:5434/aibank?sslmode=disable"
}

func coreAddr() string {
	if addr := os.Getenv("CORE_ADDR"); addr != "" {
		return addr
	}
	return "localhost:50051"
}

type fixture struct {
	srv      *httptest.Server
	core     *coreclient.Client
	store    *oauth.Store
	server   *Server
	ctx      context.Context
	clientID string
	token    string
	readOnly string
	// Cuentas ya abiertas por la integración.
	payer string
	payee string
}

func setup(t *testing.T) *fixture {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	db, err := sql.Open("postgres", databaseURL())
	if err != nil {
		t.Fatalf("abrir base: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(ctx); err != nil {
		t.Skipf("PostgreSQL no disponible: %v", err)
	}

	core, err := coreclient.Dial(ctx, coreAddr())
	if err != nil {
		t.Fatalf("dial core: %v", err)
	}
	t.Cleanup(func() { _ = core.Close() })

	suffix := uuid.NewString()
	products := map[string]string{}
	for _, currency := range []string{"USD", "PEN"} {
		code := "AGENT-" + currency + "-" + suffix
		if _, err := core.CreateProduct(ctx, coreclient.NewProduct{
			Code: code, Name: "Cuenta de agente " + currency, Currency: currency,
		}); err != nil {
			if coreclient.Retryable(err) {
				t.Skipf("core no disponible en %s: %v", coreAddr(), err)
			}
			t.Fatalf("crear producto %s: %v", currency, err)
		}
		products[currency] = code
	}

	cash, err := core.CreateInternalAccount(ctx, "sbx-cash-"+suffix, "Caja de sandbox",
		corev1.AccountType_ACCOUNT_TYPE_ASSET, "USD")
	if err != nil {
		t.Fatalf("crear caja: %v", err)
	}

	// La cuenta de retención tiene que existir para que el core pueda autorizar.
	if _, err := core.GetAccount(ctx, "AUTH-HOLDS-USD"); err != nil {
		if _, err := core.CreateInternalAccount(ctx, "AUTH-HOLDS-USD", "Retenciones",
			corev1.AccountType_ACCOUNT_TYPE_LIABILITY, "USD"); err != nil {
			t.Fatalf("crear cuenta de retención: %v", err)
		}
	}

	store := oauth.NewStore(db)
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrar oauth: %v", err)
	}

	issuer, err := oauth.NewIssuer("https://api.aibank.test",
		[]byte("clave-de-pruebas-de-32-bytes-min!"), nil)
	if err != nil {
		t.Fatalf("emisor: %v", err)
	}

	clientID := "mercatus_" + suffix
	secret, err := store.Register(ctx, clientID, "Mercatus",
		[]string{oauth.ScopePaymentsRead, oauth.ScopePaymentsWrite, oauth.ScopeAccountsWrite})
	if err != nil {
		t.Fatalf("registrar cliente: %v", err)
	}

	oauthSrv := oauth.NewServer(store, issuer, nil)
	server := NewServer(core, oauthSrv, store, Config{
		Sandbox:              true,
		ProductByCurrency:    products,
		SandboxCashAccountID: cash.Id,
	}, nil)

	srv := httptest.NewServer(server.Handler())
	srv.Client().Transport = &http.Transport{DisableKeepAlives: true}
	t.Cleanup(srv.Close)

	f := &fixture{
		srv: srv, core: core, store: store, server: server,
		ctx: ctx, clientID: clientID,
	}
	f.token = f.fetchToken(t, srv, clientID, secret, "")
	f.readOnly = f.fetchToken(t, srv, clientID, secret, oauth.ScopePaymentsRead)

	f.payer = f.openAccount(t, "agent_payer_"+suffix, 100_000000)
	f.payee = f.openAccount(t, "agent_payee_"+suffix, 0)
	return f
}

func (f *fixture) fetchToken(t *testing.T, srv *httptest.Server, clientID, secret, scope string) string {
	t.Helper()
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {clientID},
		"client_secret": {secret},
	}
	if scope != "" {
		form.Set("scope", scope)
	}
	resp, err := srv.Client().PostForm(srv.URL+"/oauth/token", form)
	if err != nil {
		t.Fatalf("pedir token: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("token status = %d: %s", resp.StatusCode, body)
	}
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out["access_token"].(string)
}

// do ejecuta una petición autenticada y devuelve status, cuerpo y cabeceras.
func (f *fixture) do(t *testing.T, method, path, token string, body any, headers map[string]string) (int, map[string]any, http.Header) {
	t.Helper()

	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, f.srv.URL+path, reader)
	if err != nil {
		t.Fatalf("construir petición: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := f.srv.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()

	var out map[string]any
	raw, _ := io.ReadAll(resp.Body)
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return resp.StatusCode, out, resp.Header
}

func (f *fixture) openAccount(t *testing.T, ownerRef string, initial int64) string {
	t.Helper()
	body := map[string]any{"owner_reference": ownerRef, "currency": "USD"}
	if initial > 0 {
		body["initial_balance"] = initial
	}
	status, out, _ := f.do(t, http.MethodPost, "/v1/accounts", f.token, body, nil)
	if status != http.StatusCreated {
		t.Fatalf("abrir cuenta: status %d, %v", status, out)
	}
	return out["account_id"].(string)
}

func (f *fixture) authorize(t *testing.T, amount int64) (int, map[string]any, http.Header) {
	return f.do(t, http.MethodPost, "/v1/authorizations", f.token, map[string]any{
		"payer_account_id": f.payer,
		"payee_account_id": f.payee,
		"amount":           amount,
		"currency":         "USD",
	}, map[string]string{"Idempotency-Key": "cart-" + uuid.NewString()})
}

// ---------------------------------------------------------------- cuentas

func TestSeAbreUnaCuentaConIdentificadorConPrefijo(t *testing.T) {
	f := setup(t)

	status, out, _ := f.do(t, http.MethodPost, "/v1/accounts", f.token, map[string]any{
		"owner_reference": "agent_" + uuid.NewString(),
		"currency":        "USD",
		"display_name":    "Mercatus · research-bot-42",
	}, nil)

	if status != http.StatusCreated {
		t.Fatalf("status = %d: %v", status, out)
	}
	id, _ := out["account_id"].(string)
	if !strings.HasPrefix(id, "acc_") {
		t.Errorf("account_id = %q, se esperaba prefijo acc_", id)
	}
	if out["currency"] != "USD" || out["status"] != "active" {
		t.Errorf("respuesta inesperada: %v", out)
	}
}

func TestAbrirDosVecesLaMismaCuentaDevuelveLaPrimera(t *testing.T) {
	f := setup(t)
	ref := "agent_" + uuid.NewString()
	body := map[string]any{"owner_reference": ref, "currency": "USD"}

	first, out1, _ := f.do(t, http.MethodPost, "/v1/accounts", f.token, body, nil)
	second, out2, _ := f.do(t, http.MethodPost, "/v1/accounts", f.token, body, nil)

	if first != http.StatusCreated {
		t.Fatalf("primera: %d", first)
	}
	// 200 y no 201: no se creó nada. Y sobre todo, la MISMA cuenta: dos cuentas
	// para el mismo agente partirían su saldo en dos.
	if second != http.StatusOK {
		t.Errorf("segunda: status = %d, se esperaba 200", second)
	}
	if out1["account_id"] != out2["account_id"] {
		t.Errorf("devolvió otra cuenta: %v vs %v", out1["account_id"], out2["account_id"])
	}
}

func TestElMismoAgenteEnOtraMonedaEsOtraCuenta(t *testing.T) {
	f := setup(t)
	ref := "agent_" + uuid.NewString()

	sUSD, usd, _ := f.do(t, http.MethodPost, "/v1/accounts", f.token,
		map[string]any{"owner_reference": ref, "currency": "USD"}, nil)
	sPEN, pen, _ := f.do(t, http.MethodPost, "/v1/accounts", f.token,
		map[string]any{"owner_reference": ref, "currency": "PEN"}, nil)

	if sUSD != http.StatusCreated || sPEN != http.StatusCreated {
		t.Fatalf("status = %d y %d: %v %v", sUSD, sPEN, usd, pen)
	}
	// Las cuentas son mono-moneda: confundirlas mezclaría dos monedas en un mismo
	// saldo, que es la clase de error que solo se descubre al cuadrar.
	if usd["account_id"] == pen["account_id"] {
		t.Error("la cuenta en PEN devolvió la cuenta en USD")
	}
	if pen["currency"] != "PEN" {
		t.Errorf("currency = %v, se esperaba PEN", pen["currency"])
	}
}

func TestUnaMonedaSinProductoSeRechaza(t *testing.T) {
	f := setup(t)

	status, out, _ := f.do(t, http.MethodPost, "/v1/accounts", f.token,
		map[string]any{"owner_reference": "agent_" + uuid.NewString(), "currency": "EUR"}, nil)

	// Abrirla bajo el producto de otra moneda sería peor que rechazarla: el error
	// aparecería mucho después, al cuadrar.
	if status != http.StatusUnprocessableEntity || out["error"] != CodeCurrencyMismatch {
		t.Fatalf("status = %d, error = %v", status, out["error"])
	}
	// El mensaje dice cuáles SÍ se admiten, para no dejar al cliente adivinando.
	if msg, _ := out["message"].(string); !strings.Contains(msg, "USD") {
		t.Errorf("el mensaje no lista las monedas admitidas: %q", msg)
	}
}

func TestElSaldoInicialSoloSeAdmiteEnSandbox(t *testing.T) {
	f := setup(t)
	f.server.cfg.Sandbox = false
	t.Cleanup(func() { f.server.cfg.Sandbox = true })

	status, out, _ := f.do(t, http.MethodPost, "/v1/accounts", f.token, map[string]any{
		"owner_reference": "agent_" + uuid.NewString(),
		"currency":        "USD",
		"initial_balance": 1_000000,
	}, nil)

	// En producción el dinero entra por el fondeo de la cuenta maestra, nunca por
	// un campo de la petición que abre la cuenta.
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, se esperaba 400: %v", status, out)
	}
}

func TestFaltaOwnerReferenceEsUnErrorExplicito(t *testing.T) {
	f := setup(t)
	status, out, _ := f.do(t, http.MethodPost, "/v1/accounts", f.token,
		map[string]any{"currency": "USD"}, nil)

	if status != http.StatusBadRequest || out["error"] != CodeMalformedRequest {
		t.Errorf("status = %d, error = %v", status, out["error"])
	}
}

func TestUnCampoDesconocidoSeRechaza(t *testing.T) {
	f := setup(t)
	status, out, _ := f.do(t, http.MethodPost, "/v1/accounts", f.token, map[string]any{
		"owner_reference": "agent_x",
		"currency":        "USD",
		"ammount":         1000, // errata deliberada
	}, nil)

	// Aceptar la errata en silencio dejaría al cliente creyendo que mandó un dato
	// que el servidor nunca vio.
	if status != http.StatusBadRequest || out["error"] != CodeMalformedRequest {
		t.Errorf("status = %d, error = %v", status, out["error"])
	}
}

// ---------------------------------------------------------------- autorizar

func TestElCaminoCompletoDePagoFunciona(t *testing.T) {
	f := setup(t)

	status, auth, _ := f.authorize(t, 1000) // $0.001, el caso que motiva todo
	if status != http.StatusCreated {
		t.Fatalf("autorizar: %d, %v", status, auth)
	}
	id, _ := auth["authorization_id"].(string)
	if !strings.HasPrefix(id, "auth_") {
		t.Errorf("authorization_id = %q, se esperaba prefijo auth_", id)
	}
	if auth["status"] != "authorized" {
		t.Errorf("status = %v", auth["status"])
	}
	if auth["expires_at"] == nil {
		t.Error("falta expires_at")
	}

	// El vendedor verifica contra el banco antes de entregar lo pagado.
	status, got, _ := f.do(t, http.MethodGet, "/v1/authorizations/"+id, f.readOnly, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("consultar: %d, %v", status, got)
	}
	if got["amount"] != float64(1000) || got["currency"] != "USD" {
		t.Errorf("monto o moneda distintos: %v", got)
	}
	if got["payee_account_id"] != f.payee {
		t.Errorf("receptor = %v, se esperaba %v", got["payee_account_id"], f.payee)
	}

	status, captured, _ := f.do(t, http.MethodPost, "/v1/authorizations/"+id+"/capture", f.token, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("capturar: %d, %v", status, captured)
	}
	if captured["status"] != "captured" || captured["settled_at"] == nil {
		t.Errorf("captura incompleta: %v", captured)
	}
}

func TestSinIdempotencyKeyNoSeAutoriza(t *testing.T) {
	f := setup(t)

	status, out, _ := f.do(t, http.MethodPost, "/v1/authorizations", f.token, map[string]any{
		"payer_account_id": f.payer,
		"payee_account_id": f.payee,
		"amount":           1000,
		"currency":         "USD",
	}, nil)

	// Sin clave, un reintento por timeout de red se convierte en un segundo cobro
	// y nadie puede distinguirlo de un pago nuevo.
	if status != http.StatusBadRequest || out["error"] != CodeMalformedRequest {
		t.Errorf("status = %d, error = %v", status, out["error"])
	}
}

func TestElReintentoConLaMismaClaveDevuelveLaMismaAutorizacion(t *testing.T) {
	f := setup(t)
	key := "cart-" + uuid.NewString()
	body := map[string]any{
		"payer_account_id": f.payer, "payee_account_id": f.payee,
		"amount": 5_000000, "currency": "USD",
	}
	headers := map[string]string{"Idempotency-Key": key}

	s1, first, _ := f.do(t, http.MethodPost, "/v1/authorizations", f.token, body, headers)
	s2, second, h2 := f.do(t, http.MethodPost, "/v1/authorizations", f.token, body, headers)

	if s1 != http.StatusCreated {
		t.Fatalf("primera: %d, %v", s1, first)
	}
	// 200 y no 201: no se creó nada.
	if s2 != http.StatusOK {
		t.Errorf("segunda: status = %d, se esperaba 200", s2)
	}
	if h2.Get("Idempotent-Replay") != "true" {
		t.Error("falta el encabezado Idempotent-Replay que hace distinguible el replay en los logs")
	}
	if first["authorization_id"] != second["authorization_id"] {
		t.Errorf("autorizaciones distintas: %v vs %v",
			first["authorization_id"], second["authorization_id"])
	}
}

func TestLaMismaClaveConOtroMontoEsUnConflicto(t *testing.T) {
	f := setup(t)
	key := "cart-" + uuid.NewString()
	headers := map[string]string{"Idempotency-Key": key}

	f.do(t, http.MethodPost, "/v1/authorizations", f.token, map[string]any{
		"payer_account_id": f.payer, "payee_account_id": f.payee,
		"amount": 1_000000, "currency": "USD",
	}, headers)

	status, out, _ := f.do(t, http.MethodPost, "/v1/authorizations", f.token, map[string]any{
		"payer_account_id": f.payer, "payee_account_id": f.payee,
		"amount": 9_000000, "currency": "USD",
	}, headers)

	// Devolver la autorización de 1 para una petición de 9 sería un pago
	// silenciosamente distinto al que se pidió.
	if status != http.StatusConflict || out["error"] != CodeIdempotencyReused {
		t.Errorf("status = %d, error = %v", status, out["error"])
	}
}

func TestSinSaldoSeDevuelve402(t *testing.T) {
	f := setup(t)

	status, out, _ := f.authorize(t, 500_000000) // el pagador tiene 100

	// 402 y no 400: la petición era correcta, lo que falta es dinero.
	if status != http.StatusPaymentRequired || out["error"] != CodeInsufficientFunds {
		t.Errorf("status = %d, error = %v", status, out["error"])
	}
}

func TestPagarseASiMismoSeRechaza(t *testing.T) {
	f := setup(t)

	status, out, _ := f.do(t, http.MethodPost, "/v1/authorizations", f.token, map[string]any{
		"payer_account_id": f.payer, "payee_account_id": f.payer,
		"amount": 1000, "currency": "USD",
	}, map[string]string{"Idempotency-Key": "cart-" + uuid.NewString()})

	if status != http.StatusUnprocessableEntity || out["error"] != CodeSameAccount {
		t.Errorf("status = %d, error = %v", status, out["error"])
	}
}

func TestUnMontoNoPositivoSeRechaza(t *testing.T) {
	f := setup(t)
	for _, amount := range []int64{0, -1} {
		status, out, _ := f.do(t, http.MethodPost, "/v1/authorizations", f.token, map[string]any{
			"payer_account_id": f.payer, "payee_account_id": f.payee,
			"amount": amount, "currency": "USD",
		}, map[string]string{"Idempotency-Key": "cart-" + uuid.NewString()})
		if status != http.StatusBadRequest {
			t.Errorf("amount %d: status = %d, %v", amount, status, out)
		}
	}
}

// ---------------------------------------------------------------- capturar

func TestCapturarDosVecesDevuelveLaMismaCaptura(t *testing.T) {
	f := setup(t)
	_, auth, _ := f.authorize(t, 2_000000)
	id := auth["authorization_id"].(string)

	s1, first, _ := f.do(t, http.MethodPost, "/v1/authorizations/"+id+"/capture", f.token, nil, nil)
	s2, second, _ := f.do(t, http.MethodPost, "/v1/authorizations/"+id+"/capture", f.token, nil, nil)

	// Es lo acordado con Mercatus: un timeout de red no se distingue de un fallo
	// real, y responder 409 obligaría al cliente a adivinar si el dinero se movió.
	if s1 != http.StatusOK || s2 != http.StatusOK {
		t.Fatalf("status = %d y %d, se esperaban 200 en ambas", s1, s2)
	}
	if first["settled_at"] != second["settled_at"] {
		t.Errorf("settled_at distinto: %v vs %v", first["settled_at"], second["settled_at"])
	}
}

func TestNoSeCapturaUnaAutorizacionVencida(t *testing.T) {
	f := setup(t)
	_, auth, _ := f.authorize(t, 2_000000)
	id := auth["authorization_id"].(string)

	db, _ := sql.Open("postgres", databaseURL())
	defer db.Close()
	raw := strings.TrimPrefix(id, "auth_")
	if _, err := db.Exec(`
		UPDATE authorizations SET expires_at = now() - interval '1 minute'
		 WHERE replace(id::text, '-', '') = $1`, raw); err != nil {
		t.Fatalf("vencer: %v", err)
	}

	status, out, _ := f.do(t, http.MethodPost, "/v1/authorizations/"+id+"/capture", f.token, nil, nil)
	if status != http.StatusConflict || out["error"] != CodeAuthorizationExpired {
		t.Errorf("status = %d, error = %v", status, out["error"])
	}

	// Y la consulta la reporta vencida, no autorizada: no se promete algo que la
	// contabilidad no vaya a cumplir.
	_, got, _ := f.do(t, http.MethodGet, "/v1/authorizations/"+id, f.token, nil, nil)
	if got["status"] != "expired" {
		t.Errorf("status consultado = %v, se esperaba expired", got["status"])
	}
}

// ---------------------------------------------------------------- reembolso

func TestReembolsarDevuelveElDinero(t *testing.T) {
	f := setup(t)
	_, auth, _ := f.authorize(t, 3_000000)
	id := auth["authorization_id"].(string)
	f.do(t, http.MethodPost, "/v1/authorizations/"+id+"/capture", f.token, nil, nil)

	status, out, _ := f.do(t, http.MethodPost, "/v1/authorizations/"+id+"/refund", f.token, nil, nil)
	if status != http.StatusOK || out["status"] != "refunded" {
		t.Fatalf("status = %d, %v", status, out)
	}

	balance, err := f.core.GetBalance(f.ctx, mustDecode(t, accountPrefix, f.payee))
	if err != nil {
		t.Fatalf("saldo: %v", err)
	}
	if balance.AmountMicros != 0 {
		t.Errorf("el receptor quedó con %d micras tras el reembolso", balance.AmountMicros)
	}
}

func TestNoSeReembolsaLoQueNuncaSeCobro(t *testing.T) {
	f := setup(t)
	_, auth, _ := f.authorize(t, 1_000000)
	id := auth["authorization_id"].(string)

	status, out, _ := f.do(t, http.MethodPost, "/v1/authorizations/"+id+"/refund", f.token, nil, nil)

	// Reembolsar una retención le daría al pagador un dinero que nunca perdió.
	if status != http.StatusConflict || out["error"] != CodeNothingToRefund {
		t.Errorf("status = %d, error = %v", status, out["error"])
	}
}

// ---------------------------------------------------------------- verificación

func TestElVendedorPuedeVerificarMontoYReceptor(t *testing.T) {
	f := setup(t)
	_, auth, _ := f.authorize(t, 1000)
	id := auth["authorization_id"].(string)

	// Coincide: la consulta responde normal.
	status, _, _ := f.do(t, http.MethodGet,
		fmt.Sprintf("/v1/authorizations/%s?expected_amount=1000&expected_payee_account_id=%s", id, f.payee),
		f.readOnly, nil, nil)
	if status != http.StatusOK {
		t.Errorf("verificación correcta rechazada: %d", status)
	}

	// Monto distinto al esperado.
	status, out, _ := f.do(t, http.MethodGet,
		"/v1/authorizations/"+id+"?expected_amount=9999", f.readOnly, nil, nil)
	if status != http.StatusUnprocessableEntity || out["error"] != CodeAmountMismatch {
		t.Errorf("monto: status = %d, error = %v", status, out["error"])
	}

	// Receptor distinto al esperado.
	status, out, _ = f.do(t, http.MethodGet,
		"/v1/authorizations/"+id+"?expected_payee_account_id="+f.payer, f.readOnly, nil, nil)
	if status != http.StatusUnprocessableEntity || out["error"] != CodeRecipientMismatch {
		t.Errorf("receptor: status = %d, error = %v", status, out["error"])
	}
}

func TestSinParametrosDeVerificacionLaConsultaResponde(t *testing.T) {
	f := setup(t)
	_, auth, _ := f.authorize(t, 1000)
	id := auth["authorization_id"].(string)

	// La verificación es OPCIONAL: quien prefiera comparar de su lado, puede.
	status, _, _ := f.do(t, http.MethodGet, "/v1/authorizations/"+id, f.readOnly, nil, nil)
	if status != http.StatusOK {
		t.Errorf("status = %d", status)
	}
}

// ---------------------------------------------------------------- autorización

func TestSinTokenNoSePasa(t *testing.T) {
	f := setup(t)
	status, out, _ := f.do(t, http.MethodGet, "/v1/authorizations/auth_"+strings.ReplaceAll(uuid.NewString(), "-", ""), "", nil, nil)
	if status != http.StatusUnauthorized || out["error"] != CodeInvalidCredential {
		t.Errorf("status = %d, error = %v", status, out["error"])
	}
}

func TestUnTokenDeSoloLecturaNoPuedeMoverDinero(t *testing.T) {
	f := setup(t)

	status, out, _ := f.do(t, http.MethodPost, "/v1/authorizations", f.readOnly, map[string]any{
		"payer_account_id": f.payer, "payee_account_id": f.payee,
		"amount": 1000, "currency": "USD",
	}, map[string]string{"Idempotency-Key": "cart-" + uuid.NewString()})

	// 403 y no 401: el token es válido, lo que falta es permiso.
	if status != http.StatusForbidden || out["error"] != CodeInsufficientScope {
		t.Errorf("status = %d, error = %v", status, out["error"])
	}
}

func TestUnaCuentaDeOtraIntegracionNoExiste(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	// Cuenta real, de OTRA integración.
	otra := "otra_" + uuid.NewString()
	if _, err := f.store.Register(ctx, otra, "Otra plataforma", []string{oauth.ScopePaymentsWrite}); err != nil {
		t.Fatalf("registrar: %v", err)
	}
	ajena := uuid.NewString()
	if _, _, err := f.store.LinkAccount(ctx, otra, oauth.SubAccount{
		AccountID: ajena, OwnerReference: "agent_ajeno", Currency: "USD",
	}); err != nil {
		t.Fatalf("enlazar: %v", err)
	}

	status, out, _ := f.do(t, http.MethodPost, "/v1/authorizations", f.token, map[string]any{
		"payer_account_id": f.payer,
		"payee_account_id": encodeID(accountPrefix, ajena),
		"amount":           1000, "currency": "USD",
	}, map[string]string{"Idempotency-Key": "cart-" + uuid.NewString()})

	// 404 y no 403: confirmar que la cuenta existe pero es ajena permitiría
	// descubrir qué cuentas hay en el banco probando identificadores.
	if status != http.StatusNotFound || out["error"] != CodeAccountNotFound {
		t.Errorf("status = %d, error = %v", status, out["error"])
	}
}

func TestUnaCuentaInexistenteYUnaMalFormadaRespondenIgual(t *testing.T) {
	f := setup(t)

	_, noExiste, _ := f.do(t, http.MethodGet,
		"/v1/accounts/"+encodeID(accountPrefix, uuid.NewString())+"/transactions", f.token, nil, nil)
	_, basura, _ := f.do(t, http.MethodGet, "/v1/accounts/acc_no-es-un-uuid/transactions", f.token, nil, nil)

	if noExiste["error"] != basura["error"] {
		t.Errorf("respuestas distintas: %v vs %v", noExiste["error"], basura["error"])
	}
}

func TestUnaAutorizacionAjenaNoSePuedeCapturar(t *testing.T) {
	f := setup(t)
	_, auth, _ := f.authorize(t, 1_000000)
	id := auth["authorization_id"].(string)

	// Otra integración, con su propio token, conoce el identificador.
	ctx := context.Background()
	otro := "otro_" + uuid.NewString()
	secret, err := f.store.Register(ctx, otro, "Otra plataforma",
		[]string{oauth.ScopePaymentsRead, oauth.ScopePaymentsWrite})
	if err != nil {
		t.Fatalf("registrar: %v", err)
	}
	tokenAjeno := f.fetchToken(t, f.srv, otro, secret, "")

	status, out, _ := f.do(t, http.MethodPost, "/v1/authorizations/"+id+"/capture", tokenAjeno, nil, nil)
	if status != http.StatusNotFound || out["error"] != CodeAuthorizationMissing {
		t.Errorf("status = %d, error = %v — otra integración capturó un pago ajeno", status, out["error"])
	}
}

func TestUnPrefijoEquivocadoNoSeInterpreta(t *testing.T) {
	f := setup(t)
	_, auth, _ := f.authorize(t, 1000)
	id := auth["authorization_id"].(string)

	// Pasar un auth_ donde va un acc_ es un error del cliente que conviene
	// señalar, no interpretar como si fuera el identificador correcto.
	status, _, _ := f.do(t, http.MethodGet, "/v1/accounts/"+id+"/transactions", f.token, nil, nil)
	if status != http.StatusNotFound {
		t.Errorf("status = %d, se esperaba 404", status)
	}
}

// ---------------------------------------------------------------- extracto

func TestElExtractoMuestraLosMovimientosDeLaCuenta(t *testing.T) {
	f := setup(t)
	_, auth, _ := f.authorize(t, 4_000000)
	id := auth["authorization_id"].(string)
	f.do(t, http.MethodPost, "/v1/authorizations/"+id+"/capture", f.token, nil, nil)

	status, out, _ := f.do(t, http.MethodGet, "/v1/accounts/"+f.payee+"/transactions", f.readOnly, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d: %v", status, out)
	}

	txs, _ := out["transactions"].([]any)
	if len(txs) == 0 {
		t.Fatal("el receptor cobró pero su extracto está vacío")
	}
	first, _ := txs[0].(map[string]any)
	if first["direction"] != "credit" {
		t.Errorf("direction = %v, se esperaba credit en el receptor", first["direction"])
	}
	if first["amount"] != float64(4_000000) {
		t.Errorf("amount = %v", first["amount"])
	}
}

func TestUnLimitFueraDeRangoSeRechaza(t *testing.T) {
	f := setup(t)
	for _, limit := range []string{"0", "-1", "500", "abc"} {
		status, _, _ := f.do(t, http.MethodGet,
			"/v1/accounts/"+f.payer+"/transactions?limit="+limit, f.token, nil, nil)
		if status != http.StatusBadRequest {
			t.Errorf("limit=%s: status = %d, se esperaba 400", limit, status)
		}
	}
}

// ---------------------------------------------------------------- límite de tasa

func TestElLimiteDeTasaDevuelve429ConRetryAfter(t *testing.T) {
	f := setup(t)
	f.server.byClient = NewLimiter(1, 1)

	f.do(t, http.MethodGet, "/v1/accounts/"+f.payer+"/transactions", f.token, nil, nil)
	status, out, headers := f.do(t, http.MethodGet, "/v1/accounts/"+f.payer+"/transactions", f.token, nil, nil)

	if status != http.StatusTooManyRequests || out["error"] != CodeRateLimited {
		t.Fatalf("status = %d, error = %v", status, out["error"])
	}
	// Sin Retry-After el cliente adivina el backoff, y adivinar mal es lo que
	// convierte un pico en una caída.
	if headers.Get("Retry-After") == "" {
		t.Error("falta el encabezado Retry-After")
	}
}

func TestElEndpointDelTokenTambienEstaLimitado(t *testing.T) {
	f := setup(t)
	f.server.byClient = NewLimiter(1, 1)

	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {"x"}, "client_secret": {"y"}}
	_, _ = f.srv.Client().PostForm(f.srv.URL+"/oauth/token", form)
	resp, err := f.srv.Client().PostForm(f.srv.URL+"/oauth/token", form)
	if err != nil {
		t.Fatalf("pedir token: %v", err)
	}
	defer resp.Body.Close()

	// Es el único sitio donde se puede probar un secreto: sin límite queda abierto
	// a fuerza bruta.
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("status = %d, se esperaba 429", resp.StatusCode)
	}
}

func TestElCuboDeFichasSeRellenaConElTiempo(t *testing.T) {
	limiter := NewLimiter(10, 2)
	base := time.Now()
	limiter.now = func() time.Time { return base }

	if ok, _ := limiter.Allow("k"); !ok {
		t.Fatal("la primera petición debería pasar")
	}
	limiter.Allow("k")
	if ok, wait := limiter.Allow("k"); ok || wait <= 0 {
		t.Fatalf("la tercera debería rebotar con espera, ok=%v wait=%v", ok, wait)
	}

	// A 10 por segundo, un décimo de segundo devuelve una ficha.
	limiter.now = func() time.Time { return base.Add(150 * time.Millisecond) }
	if ok, _ := limiter.Allow("k"); !ok {
		t.Error("el cubo no se rellenó con el paso del tiempo")
	}
}

func TestElLimitePorCuentaNoAfectaAOtraCuenta(t *testing.T) {
	limiter := NewLimiter(1, 1)
	limiter.Allow("cuenta-a")

	// Un agente en bucle no puede consumir la cuota de los demás.
	if ok, _ := limiter.Allow("cuenta-b"); !ok {
		t.Error("una cuenta agotó la cuota de otra")
	}
}

// ---------------------------------------------------------------- identificadores

func TestLosIdentificadoresVanYVuelven(t *testing.T) {
	id := uuid.NewString()

	public := encodeID(accountPrefix, id)
	if !strings.HasPrefix(public, "acc_") || strings.Contains(public, "-") {
		t.Errorf("identificador público = %q", public)
	}

	back, err := decodeID(accountPrefix, public)
	if err != nil || back != id {
		t.Errorf("volvió %q (err %v), se esperaba %q", back, err, id)
	}

	// El UUID desnudo también se acepta: durante la integración es cómodo pegar
	// uno que se vio en un log del banco.
	if back, err := decodeID(accountPrefix, id); err != nil || back != id {
		t.Errorf("no aceptó el UUID desnudo: %v", err)
	}

	if _, err := decodeID(accountPrefix, "acc_no-es-uuid"); err == nil {
		t.Error("aceptó un identificador inválido")
	}
	if _, err := decodeID(accountPrefix, encodeID(authorizationPrefix, id)); err == nil {
		t.Error("aceptó un auth_ donde va un acc_")
	}
}

func mustDecode(t *testing.T, prefix, public string) string {
	t.Helper()
	id, err := decodeID(prefix, public)
	if err != nil {
		t.Fatalf("decodificar %q: %v", public, err)
	}
	return id
}
