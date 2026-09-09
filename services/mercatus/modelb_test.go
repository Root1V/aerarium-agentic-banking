package mercatus

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aibank/aibank/clients/go/coreclient"
	"github.com/aibank/aibank/services/bff"
	bffsim "github.com/aibank/aibank/services/bff/sim"
	"github.com/aibank/aibank/services/oauth"
	"github.com/google/uuid"
)

// El modelo B de punta a punta: la plataforma pide permiso, el TITULAR lo
// concede desde su app, y recién entonces la plataforma puede pagar desde una
// cuenta que no es suya.
//
// Se prueba con los dos servicios reales —la API de socio y el canal del
// titular— porque el reparto entre ambos es justamente lo que da la garantía: si
// la plataforma pudiera conceder su propio permiso, el mandato no probaría nada.

type modelBFixture struct {
	*fixture
	// El canal del titular.
	bff     *httptest.Server
	bffAuth *bffsim.Authenticator
	// El titular y su cuenta de propósito para el agente.
	holderID    string
	holderToken string
	agentAcct   string
}

func setupModelB(t *testing.T) *modelBFixture {
	t.Helper()
	f := setup(t)

	db, err := sql.Open("postgres", databaseURL())
	if err != nil {
		t.Fatalf("abrir base: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := oauth.NewStore(db)

	// El servicio ya tiene un store; se le enchufa al BFF el mismo, que es lo que
	// une los dos lados del flujo.
	bffAuth := bffsim.NewAuthenticator()
	bffServer := bff.NewServer(f.core, bffAuth, nil).WithConsents(store)
	ts := httptest.NewServer(bffServer.Handler())
	ts.Client().Transport = &http.Transport{DisableKeepAlives: true}
	t.Cleanup(ts.Close)

	// Un cliente del banco, con su cuenta de propósito para el agente. En el
	// modelo B esta cuenta es SUYA, no de la plataforma.
	suffix := uuid.NewString()
	holderID := uuid.NewString()
	account, err := f.core.OpenCustomerAccount(f.ctx, "holder-"+suffix, "Bolsillo del agente",
		holderID, f.productCode)
	if err != nil {
		t.Fatalf("abrir cuenta del titular: %v", err)
	}
	if _, err := f.core.Post(f.ctx, "fund-"+suffix, "deposit", []coreclient.Entry{
		coreclient.Debit(f.cashID, 50_000000, "USD"),
		coreclient.Credit(account.Id, 50_000000, "USD"),
	}, "fondeo del bolsillo"); err != nil {
		t.Fatalf("fondear: %v", err)
	}

	return &modelBFixture{
		fixture:     f,
		bff:         ts,
		bffAuth:     bffAuth,
		holderID:    holderID,
		holderToken: bffAuth.Issue(holderID, "device-"+holderID),
		agentAcct:   account.Id,
	}
}

// requestConsent: la plataforma pide permiso.
func (f *modelBFixture) requestConsent(t *testing.T, maxPerOp, maxTotal *int64) map[string]any {
	t.Helper()
	body := map[string]any{"currency": "USD", "purpose": "Pagos del agente de investigación"}
	if maxPerOp != nil {
		body["max_per_operation"] = *maxPerOp
	}
	if maxTotal != nil {
		body["max_total"] = *maxTotal
	}
	status, out, _ := f.do(t, http.MethodPost, "/v1/consent-requests", f.token, body, nil)
	if status != http.StatusCreated {
		t.Fatalf("pedir consentimiento: %d, %v", status, out)
	}
	return out
}

// approveConsent: el titular concede, desde SU app.
func (f *modelBFixture) approveConsent(t *testing.T, handoff string, maxPerOp, maxTotal *int64, days int) (int, map[string]any) {
	t.Helper()
	body := map[string]any{"account_id": f.agentAcct, "expires_in_days": days}
	if maxPerOp != nil {
		body["max_per_operation"] = *maxPerOp
	}
	if maxTotal != nil {
		body["max_total"] = *maxTotal
	}
	return f.callBFF(t, http.MethodPost, "/v1/consent-requests/"+handoff+"/approve", f.holderToken, body)
}

func i64(v int64) *int64 { return &v }

// ---------------------------------------------------------------- flujo completo

func TestElModeloBFuncionaDePuntaAPunta(t *testing.T) {
	f := setupModelB(t)

	// 1. La plataforma pide. No nombra la cuenta: no la conoce todavía.
	request := f.requestConsent(t, i64(10_000000), i64(30_000000))
	if request["status"] != "pending" {
		t.Fatalf("status = %v", request["status"])
	}
	handoff, _ := request["handoff_code"].(string)
	if handoff == "" {
		t.Fatal("falta handoff_code: sin él el titular no puede encontrar la solicitud")
	}

	// 2. El titular ve qué le piden, en la app del banco.
	status, prompt := f.callBFF(t, http.MethodGet, "/v1/consent-requests/"+handoff, f.holderToken, nil)
	if status != http.StatusOK {
		t.Fatalf("consultar solicitud: %d, %v", status, prompt)
	}
	if prompt["purpose"] != "Pagos del agente de investigación" {
		t.Errorf("purpose = %v", prompt["purpose"])
	}
	// El nombre registrado de la integración, no uno que ella eligió.
	if prompt["requested_by"] != "Mercatus" {
		t.Errorf("requested_by = %v, se esperaba el nombre registrado", prompt["requested_by"])
	}

	// 3. El titular concede, con topes propios más bajos que los pedidos.
	status, mandate := f.approveConsent(t, handoff, i64(5_000000), i64(20_000000), 30)
	if status != http.StatusCreated {
		t.Fatalf("aprobar: %d, %v", status, mandate)
	}
	mandateID, _ := mandate["mandate_id"].(string)
	if mandate["status"] != "active" {
		t.Errorf("status = %v", mandate["status"])
	}

	// 4. La plataforma consulta y descubre que ya tiene permiso.
	status, resolved, _ := f.do(t, http.MethodGet,
		"/v1/consent-requests/"+request["consent_request_id"].(string), f.token, nil, nil)
	if status != http.StatusOK || resolved["status"] != "approved" {
		t.Fatalf("consultar: %d, %v", status, resolved)
	}
	if resolved["mandate_id"] != mandateID {
		t.Errorf("mandate_id = %v, se esperaba %v", resolved["mandate_id"], mandateID)
	}

	// 5. Y paga desde la cuenta del titular. Mismo contrato de pago que el
	// modelo A: lo único que cambia es de dónde sale la autoridad.
	status, auth, _ := f.do(t, http.MethodPost, "/v1/authorizations", f.token, map[string]any{
		"mandate_id":       mandateID,
		"payer_account_id": resolved["account_id"],
		"payee_account_id": f.payee,
		"amount":           3_000000,
		"currency":         "USD",
	}, map[string]string{"Idempotency-Key": "cart-" + uuid.NewString()})
	if status != http.StatusCreated {
		t.Fatalf("autorizar bajo mandato: %d, %v", status, auth)
	}

	authID := auth["authorization_id"].(string)
	status, captured, _ := f.do(t, http.MethodPost, "/v1/authorizations/"+authID+"/capture", f.token, nil, nil)
	if status != http.StatusOK || captured["status"] != "captured" {
		t.Fatalf("capturar: %d, %v", status, captured)
	}

	// El dinero salió de la cuenta del titular, que nunca dejó de ser suya.
	balance, err := f.core.GetBalance(f.ctx, f.agentAcct)
	if err != nil {
		t.Fatalf("saldo: %v", err)
	}
	if balance.AmountMicros != 47_000000 {
		t.Errorf("saldo = %d, se esperaban 47_000000", balance.AmountMicros)
	}
}

// ---------------------------------------------------------------- lo que impide

func TestLaPlataformaNoPuedeConcederseSuPropioPermiso(t *testing.T) {
	f := setupModelB(t)
	request := f.requestConsent(t, nil, nil)
	handoff := request["handoff_code"].(string)

	// Con su token de socio, contra el canal del titular. Es el ataque que hace
	// que todo el diseño tenga sentido: si esto funcionara, el mandato no
	// probaría que una persona autorizó nada.
	status, _ := f.callBFF(t, http.MethodPost, "/v1/consent-requests/"+handoff+"/approve",
		f.token, map[string]any{"account_id": f.agentAcct, "expires_in_days": 30})

	if status != http.StatusUnauthorized {
		t.Errorf("status = %d, se esperaba 401: el token de socio no vale en el canal del titular", status)
	}
}

func TestElTitularNoPuedeConcederMasDeLoPedido(t *testing.T) {
	f := setupModelB(t)
	request := f.requestConsent(t, i64(5_000000), i64(10_000000))
	handoff := request["handoff_code"].(string)

	status, out := f.approveConsent(t, handoff, i64(50_000000), i64(10_000000), 30)

	// Conceder más de lo pedido convertiría la pantalla de consentimiento en un
	// lugar donde se otorga algo que nadie solicitó.
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, se esperaba 422: %v", status, out)
	}
	if out["code"] != "limit_above_requested" {
		t.Errorf("code = %v", out["code"])
	}
}

func TestNadiePuedeConcederPermisoSobreLaCuentaDeOtro(t *testing.T) {
	f := setupModelB(t)
	request := f.requestConsent(t, nil, nil)
	handoff := request["handoff_code"].(string)

	// Otro cliente del banco intenta conceder sobre la cuenta del primero.
	intruso := uuid.NewString()
	tokenIntruso := f.bffAuth.Issue(intruso, "device-"+intruso)

	status, _ := f.callBFF(t, http.MethodPost, "/v1/consent-requests/"+handoff+"/approve",
		tokenIntruso, map[string]any{"account_id": f.agentAcct, "expires_in_days": 30})

	if status != http.StatusNotFound {
		t.Errorf("status = %d, se esperaba 404", status)
	}
}

func TestUnPagoQueSuperaElTopeSeRechazaAunqueHayaSaldo(t *testing.T) {
	f := setupModelB(t)
	mandateID := f.grantMandate(t, i64(5_000000), i64(20_000000))

	status, out, _ := f.do(t, http.MethodPost, "/v1/authorizations", f.token, map[string]any{
		"mandate_id":       mandateID,
		"payer_account_id": encodeID(accountPrefix, f.agentAcct),
		"payee_account_id": f.payee,
		"amount":           9_000000, // hay 50 de saldo, pero el tope es 5
		"currency":         "USD",
	}, map[string]string{"Idempotency-Key": "cart-" + uuid.NewString()})

	// 422 y no 402: hay dinero, lo que falta es permiso. Son problemas distintos
	// con soluciones distintas.
	if status != http.StatusUnprocessableEntity || out["error"] != CodeMandateLimitExceeded {
		t.Errorf("status = %d, error = %v", status, out["error"])
	}
}

func TestTrasRevocarNoSePuedePagar(t *testing.T) {
	f := setupModelB(t)
	mandateID := f.grantMandate(t, nil, nil)

	// El titular retira el permiso desde su app.
	status, _ := f.callBFF(t, http.MethodPost, "/v1/mandates/"+mandateID+"/revoke", f.holderToken, nil)
	if status != http.StatusOK {
		t.Fatalf("revocar: %d", status)
	}

	status, out, _ := f.do(t, http.MethodPost, "/v1/authorizations", f.token, map[string]any{
		"mandate_id":       mandateID,
		"payer_account_id": encodeID(accountPrefix, f.agentAcct),
		"payee_account_id": f.payee,
		"amount":           1_000000,
		"currency":         "USD",
	}, map[string]string{"Idempotency-Key": "cart-" + uuid.NewString()})

	if status != http.StatusForbidden || out["error"] != CodeMandateRevoked {
		t.Errorf("status = %d, error = %v", status, out["error"])
	}
}

func TestUnMandatoAjenoNoExisteParaOtraPlataforma(t *testing.T) {
	f := setupModelB(t)
	mandateID := f.grantMandate(t, nil, nil)

	// Otra integración, con su propio token, conoce el identificador.
	otro := "otro_" + uuid.NewString()
	secret, err := f.store.Register(context.Background(), otro, "Otra plataforma",
		[]string{oauth.ScopePaymentsRead, oauth.ScopePaymentsWrite})
	if err != nil {
		t.Fatalf("registrar: %v", err)
	}
	tokenAjeno := f.fetchToken(t, f.srv, otro, secret, "")

	status, out, _ := f.do(t, http.MethodGet, "/v1/mandates/"+mandateID, tokenAjeno, nil, nil)
	if status != http.StatusNotFound || out["error"] != CodeMandateNotFound {
		t.Errorf("status = %d, error = %v — otra plataforma vio un mandato ajeno", status, out["error"])
	}

	// Y tampoco puede usarlo para pagar.
	status, out, _ = f.do(t, http.MethodPost, "/v1/authorizations", tokenAjeno, map[string]any{
		"mandate_id":       mandateID,
		"payer_account_id": encodeID(accountPrefix, f.agentAcct),
		"payee_account_id": f.payee,
		"amount":           1_000000,
		"currency":         "USD",
	}, map[string]string{"Idempotency-Key": "cart-" + uuid.NewString()})
	if status != http.StatusNotFound {
		t.Errorf("status = %d, se esperaba 404 al pagar con un mandato ajeno", status)
	}
}

func TestElPagadorTieneQueCoincidirConElDelMandato(t *testing.T) {
	f := setupModelB(t)
	mandateID := f.grantMandate(t, nil, nil)

	// Se declara otra cuenta como pagadora. No se corrige en silencio: si la
	// plataforma cree estar pagando desde otra cuenta, es un error suyo que
	// conviene que vea.
	status, out, _ := f.do(t, http.MethodPost, "/v1/authorizations", f.token, map[string]any{
		"mandate_id":       mandateID,
		"payer_account_id": f.payer,
		"payee_account_id": f.payee,
		"amount":           1_000000,
		"currency":         "USD",
	}, map[string]string{"Idempotency-Key": "cart-" + uuid.NewString()})

	if status != http.StatusForbidden || out["error"] != CodeMandateAccountScope {
		t.Errorf("status = %d, error = %v", status, out["error"])
	}
}

func TestUnaSolicitudSeResuelveUnaSolaVez(t *testing.T) {
	f := setupModelB(t)
	request := f.requestConsent(t, nil, nil)
	handoff := request["handoff_code"].(string)

	if status, out := f.approveConsent(t, handoff, nil, nil, 30); status != http.StatusCreated {
		t.Fatalf("primera aprobación: %d, %v", status, out)
	}
	status, out := f.approveConsent(t, handoff, nil, nil, 30)

	// Aprobar dos veces otorgaría dos permisos donde la persona quiso dar uno.
	if status != http.StatusConflict || out["code"] != "already_resolved" {
		t.Errorf("status = %d, code = %v", status, out["code"])
	}
}

func TestUnaSolicitudRechazadaNoOtorgaNada(t *testing.T) {
	f := setupModelB(t)
	request := f.requestConsent(t, nil, nil)
	handoff := request["handoff_code"].(string)

	if status, _ := f.callBFF(t, http.MethodPost,
		"/v1/consent-requests/"+handoff+"/reject", f.holderToken, nil); status != http.StatusNoContent {
		t.Fatalf("rechazar: %d", status)
	}

	status, out, _ := f.do(t, http.MethodGet,
		"/v1/consent-requests/"+request["consent_request_id"].(string), f.token, nil, nil)
	if out["status"] != "rejected" {
		t.Errorf("status = %v", out["status"])
	}
	if out["mandate_id"] != nil {
		t.Errorf("una solicitud rechazada devolvió un mandato: %v", out["mandate_id"])
	}
	_ = status
}

func TestSinPropositoNoSePuedePedirConsentimiento(t *testing.T) {
	f := setupModelB(t)

	status, out, _ := f.do(t, http.MethodPost, "/v1/consent-requests", f.token, map[string]any{
		"currency": "USD",
	}, nil)

	// Sin propósito, la pantalla le pide a una persona que apruebe algo que no
	// puede evaluar.
	if status != http.StatusBadRequest || out["error"] != CodeMalformedRequest {
		t.Errorf("status = %d, error = %v", status, out["error"])
	}
}

func TestElTitularVeYRetiraSusPermisos(t *testing.T) {
	f := setupModelB(t)
	mandateID := f.grantMandate(t, nil, i64(20_000000))

	status, list := f.callBFF(t, http.MethodGet, "/v1/mandates", f.holderToken, nil)
	if status != http.StatusOK {
		t.Fatalf("listar: %d", status)
	}
	mandates, _ := list["mandates"].([]any)
	if len(mandates) != 1 {
		t.Fatalf("se esperaba 1 permiso, hay %d", len(mandates))
	}
	first := mandates[0].(map[string]any)
	if first["granted_to"] != f.clientID {
		t.Errorf("granted_to = %v", first["granted_to"])
	}
	if first["remaining"] != float64(20_000000) {
		t.Errorf("remaining = %v", first["remaining"])
	}

	// Sin poder verlos, revocar sería imposible en la práctica.
	status, revoked := f.callBFF(t, http.MethodPost, "/v1/mandates/"+mandateID+"/revoke", f.holderToken, nil)
	if status != http.StatusOK || revoked["status"] != "revoked" {
		t.Errorf("revocar: %d, %v", status, revoked)
	}
}

func TestNadiePuedeRevocarElPermisoDeOtro(t *testing.T) {
	f := setupModelB(t)
	mandateID := f.grantMandate(t, nil, nil)

	intruso := uuid.NewString()
	status, _ := f.callBFF(t, http.MethodPost, "/v1/mandates/"+mandateID+"/revoke",
		f.bffAuth.Issue(intruso, "device-"+intruso), nil)

	if status != http.StatusNotFound {
		t.Errorf("status = %d, se esperaba 404", status)
	}
}

func TestElConsumoDelMandatoSeVeDesdeLaPlataforma(t *testing.T) {
	f := setupModelB(t)
	mandateID := f.grantMandate(t, nil, i64(20_000000))

	f.do(t, http.MethodPost, "/v1/authorizations", f.token, map[string]any{
		"mandate_id":       mandateID,
		"payer_account_id": encodeID(accountPrefix, f.agentAcct),
		"payee_account_id": f.payee,
		"amount":           8_000000,
		"currency":         "USD",
	}, map[string]string{"Idempotency-Key": "cart-" + uuid.NewString()})

	status, out, _ := f.do(t, http.MethodGet, "/v1/mandates/"+mandateID, f.token, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("consultar mandato: %d, %v", status, out)
	}
	// Saber cuánto queda le permite a la plataforma pedir una ampliación ANTES de
	// que un pago falle delante de un usuario.
	if out["consumed"] != float64(8_000000) || out["remaining"] != float64(12_000000) {
		t.Errorf("consumed = %v, remaining = %v", out["consumed"], out["remaining"])
	}
}

// ---------------------------------------------------------------- utilidades

func (f *modelBFixture) grantMandate(t *testing.T, maxPerOp, maxTotal *int64) string {
	t.Helper()
	request := f.requestConsent(t, maxPerOp, maxTotal)
	status, mandate := f.approveConsent(t, request["handoff_code"].(string), maxPerOp, maxTotal, 30)
	if status != http.StatusCreated {
		t.Fatalf("otorgar mandato: %d, %v", status, mandate)
	}
	return mandate["mandate_id"].(string)
}
