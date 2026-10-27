package bff_test

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
)

// La confirmación reforzada por HTTP: un teléfono vinculado con saldo de prueba
// envía dinero, y las operaciones de riesgo piden firmar con la cara.

// linkedWithFunds da de alta a alguien, vincula su teléfono y le pone saldo de
// prueba. Devuelve el token, su cuenta y el destinatario de prueba.
func (e *passkeyEnv) linkedWithFunds(t *testing.T) (h *handset, token, account, payee string) {
	t.Helper()
	reference, _, account := e.approve(t)
	h = newHandset()
	status, linked := e.link(t, h, reference, "", "482916")
	if status != http.StatusCreated {
		t.Fatalf("vincular: %d %v", status, linked)
	}
	token = linked["token"].(string)

	status, funds := e.call(t, http.MethodPost, "/v1/sandbox/funds", token, map[string]any{"account_id": account})
	if status != http.StatusOK {
		t.Fatalf("saldo de prueba: %d %v", status, funds)
	}
	return h, token, account, funds["payee"].(map[string]any)["id"].(string)
}

func (e *passkeyEnv) send(t *testing.T, token, key, from, to string, amount int64, confirmation map[string]any) (int, map[string]any) {
	t.Helper()
	body := map[string]any{
		"from_account_id": from, "to_account_id": to,
		"amount_micros": amount, "currency": "USD", "description": "prueba",
	}
	if confirmation != nil {
		body["confirmation"] = confirmation
	}
	return e.callWith(t, http.MethodPost, "/v1/transfers", token, map[string]string{"Idempotency-Key": key}, body)
}

func (e *passkeyEnv) balance(t *testing.T, token, account string) float64 {
	t.Helper()
	_, home := e.call(t, http.MethodGet, "/v1/home?account_id="+account, token, nil)
	return home["balance"].(map[string]any)["amount_micros"].(float64)
}

func TestEnviarAAlguienNuevoPideLaCaraYLuegoYaNo(t *testing.T) {
	e := newPasskeyEnv(t)
	h, token, account, payee := e.linkedWithFunds(t)
	if got := e.balance(t, token, account); got != 250_000_000 {
		t.Fatalf("saldo de prueba = %v, se esperaban 250", got)
	}

	// Primera vez a este destinatario: no se mueve nada, se pide confirmar.
	key := uuid.NewString()
	status, need := e.send(t, token, key, account, payee, 50_000_000, nil)
	if status != http.StatusPreconditionRequired || need["code"] != "confirmation_required" || need["reason"] != "new_payee" {
		t.Fatalf("primer envío: %d %v", status, need)
	}
	summary := need["summary"].(map[string]any)
	if summary["to_name"] != "Juan Pérez (prueba)" || summary["amount_micros"] != float64(50_000_000) {
		t.Errorf("lo que se le muestra a la persona: %v", summary)
	}
	if got := e.balance(t, token, account); got != 250_000_000 {
		t.Fatalf("pedir confirmación movió dinero: saldo %v", got)
	}

	// La misma petición, firmada con la cara.
	status, done := e.send(t, token, key, account, payee, 50_000_000, map[string]any{
		"challenge_id": need["challenge_id"], "method": "biometric",
		"signature": signB64(t, h.bio, need["payload"]),
	})
	if status != http.StatusCreated {
		t.Fatalf("envío confirmado: %d %v", status, done)
	}
	if got := e.balance(t, token, account); got != 200_000_000 {
		t.Errorf("saldo tras enviar 50 = %v", got)
	}

	// Ahora es conocido: un envío chico ya no pide la cara.
	if status, out := e.send(t, token, uuid.NewString(), account, payee, 10_000_000, nil); status != http.StatusCreated {
		t.Errorf("segundo envío chico: %d %v", status, out)
	}
	// Pero desde el umbral, sí, aunque sea conocido.
	if status, out := e.send(t, token, uuid.NewString(), account, payee, 200_000_000, nil); status != http.StatusPreconditionRequired || out["reason"] != "amount" {
		t.Errorf("envío grande: %d %v", status, out)
	}
}

// La firma de 50 no sirve para mover 900, aunque se reuse todo lo demás.
func TestLaConfirmacionNoSirveParaOtroMonto(t *testing.T) {
	e := newPasskeyEnv(t)
	h, token, account, payee := e.linkedWithFunds(t)
	key := uuid.NewString()
	_, need := e.send(t, token, key, account, payee, 50_000_000, nil)
	confirmation := map[string]any{
		"challenge_id": need["challenge_id"], "method": "biometric",
		"signature": signB64(t, h.bio, need["payload"]),
	}
	status, out := e.send(t, token, key, account, payee, 900_000_000, confirmation)
	if status != http.StatusForbidden || out["code"] != "confirmation_rejected" {
		t.Errorf("monto cambiado: %d %v", status, out)
	}
	if got := e.balance(t, token, account); got != 250_000_000 {
		t.Errorf("se movió dinero con una confirmación ajena: saldo %v", got)
	}
}

func TestConfirmarConLaClave(t *testing.T) {
	e := newPasskeyEnv(t)
	h, token, account, payee := e.linkedWithFunds(t)
	key := uuid.NewString()
	_, need := e.send(t, token, key, account, payee, 50_000_000, nil)

	status, out := e.send(t, token, key, account, payee, 50_000_000, map[string]any{
		"challenge_id": need["challenge_id"], "method": "pin", "pin": "135790",
		"signature": signB64(t, h.possession, need["payload"]),
	})
	if status != http.StatusUnauthorized || out["code"] != "pin_incorrect" {
		t.Fatalf("clave mala: %d %v", status, out)
	}

	_, need = e.send(t, token, key, account, payee, 50_000_000, nil)
	status, out = e.send(t, token, key, account, payee, 50_000_000, map[string]any{
		"challenge_id": need["challenge_id"], "method": "pin", "pin": "482916",
		"signature": signB64(t, h.possession, need["payload"]),
	})
	if status != http.StatusCreated {
		t.Errorf("clave buena: %d %v", status, out)
	}
}

// Sin la ruta montada —fuera del sandbox— no hay saldo de prueba.
func TestFueraDelSandboxNoHaySaldoDePrueba(t *testing.T) {
	f := setup(t)
	resp, _ := f.do(t, http.MethodGet, "/v1/sandbox", "", nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /v1/sandbox = %d, se esperaba 404", resp.StatusCode)
	}
	resp, _ = f.do(t, http.MethodPost, "/v1/sandbox/funds", f.alice.token, map[string]any{"account_id": f.alice.accountID}, nil)
	if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST /v1/sandbox/funds = %d", resp.StatusCode)
	}
}

func TestLosDestinatariosRecientes(t *testing.T) {
	e := newPasskeyEnv(t)
	h, token, account, payee := e.linkedWithFunds(t)

	if status, out := e.call(t, http.MethodGet, "/v1/payees/recent", token, nil); status != http.StatusOK || len(out["payees"].([]any)) != 0 {
		t.Fatalf("sin envíos: %d %v", status, out)
	}

	key := uuid.NewString()
	_, need := e.send(t, token, key, account, payee, 50_000_000, nil)
	e.send(t, token, key, account, payee, 50_000_000, map[string]any{
		"challenge_id": need["challenge_id"], "method": "biometric",
		"signature": signB64(t, h.bio, need["payload"]),
	})

	_, out := e.call(t, http.MethodGet, "/v1/payees/recent", token, nil)
	payees := out["payees"].([]any)
	if len(payees) != 1 {
		t.Fatalf("recientes = %v", payees)
	}
	first := payees[0].(map[string]any)
	if first["id"] != payee || first["name"] != "Juan Pérez (prueba)" {
		t.Errorf("reciente = %v", first)
	}

	// Pedir confirmación sin darla no lo hace reciente. El destinatario de prueba
	// de otra cuenta es, para esta, alguien nuevo.
	_, _, _, stranger := e.linkedWithFunds(t)
	e.send(t, token, uuid.NewString(), account, stranger, 10_000_000, nil)
	if _, out := e.call(t, http.MethodGet, "/v1/payees/recent", token, nil); len(out["payees"].([]any)) != 1 {
		t.Errorf("un envío sin confirmar entró en recientes: %v", out["payees"])
	}
}

// Pedir saldo de prueba varias veces da siempre el mismo destinatario, con el
// mismo celular: los recientes no se llenan de Juanes repetidos.
func TestElDestinatarioDePruebaEsSiempreElMismo(t *testing.T) {
	e := newPasskeyEnv(t)
	_, token, account, payee := e.linkedWithFunds(t)
	_, first := e.call(t, http.MethodPost, "/v1/sandbox/funds", token, map[string]any{"account_id": account})
	_, second := e.call(t, http.MethodPost, "/v1/sandbox/funds", token, map[string]any{"account_id": account})

	for _, funds := range []map[string]any{first, second} {
		if funds["payee"].(map[string]any)["id"] != payee {
			t.Errorf("otro destinatario: %v, se esperaba %s", funds["payee"], payee)
		}
	}
	if first["payee_phone"] == "" || first["payee_phone"] != second["payee_phone"] {
		t.Errorf("el celular cambió: %v y %v", first["payee_phone"], second["payee_phone"])
	}
	if got := e.balance(t, token, account); got != 750_000_000 {
		t.Errorf("tres saldos de prueba = %v, se esperaban 750", got)
	}
}
