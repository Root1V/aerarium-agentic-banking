package bff_test

import (
	"fmt"
	"math/rand/v2"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

// Enviar a un celular por HTTP: registrar el propio con el código, encontrar el
// de otro y ver en el historial a quién se le envió.

func randomPhone() string { return fmt.Sprintf("9%08d", rand.IntN(100_000_000)) }

func TestRegistrarMiNumeroYQueOtroMeEncuentre(t *testing.T) {
	e := newPasskeyEnv(t)
	_, token, account, _ := e.linkedWithFunds(t)
	number := randomPhone()

	status, started := e.call(t, http.MethodPost, "/v1/aliases/phone", token, map[string]any{"account_id": account, "phone": number})
	if status != http.StatusOK || started["sandbox_code"] == "" {
		t.Fatalf("pedir código: %d %v", status, started)
	}
	status, wrong := e.call(t, http.MethodPost, "/v1/aliases/phone/verify", token, map[string]any{
		"verification_id": started["verification_id"], "code": "x",
	})
	if status != http.StatusBadRequest || wrong["code"] != "code_incorrect" || wrong["remaining_attempts"] != float64(4) {
		t.Fatalf("código malo: %d %v", status, wrong)
	}
	status, done := e.call(t, http.MethodPost, "/v1/aliases/phone/verify", token, map[string]any{
		"verification_id": started["verification_id"], "code": started["sandbox_code"],
	})
	if status != http.StatusOK || done["phone"] != "+51"+number {
		t.Fatalf("verificar: %d %v", status, done)
	}
	if status, mine := e.call(t, http.MethodGet, "/v1/aliases/phone", token, nil); status != http.StatusOK || mine["account_id"] != account {
		t.Errorf("mi número: %d %v", status, mine)
	}

	// Otra persona lo encuentra, con el nombre a medias.
	_, other, _, _ := e.linkedWithFunds(t)
	status, found := e.call(t, http.MethodGet, "/v1/directory/phone/"+number, other, nil)
	if status != http.StatusOK {
		t.Fatalf("buscar: %d %v", status, found)
	}
	got := found["account"].(map[string]any)
	if got["id"] != account || got["name"] != "Ana P." {
		t.Errorf("encontrado = %v", got)
	}
}

// No se puede registrar un número para que llegue a la cuenta de otro.
func TestElNumeroSoloLlegaAUnaCuentaPropia(t *testing.T) {
	e := newPasskeyEnv(t)
	_, token, _, _ := e.linkedWithFunds(t)
	_, _, otherAccount, _ := e.linkedWithFunds(t)
	status, _ := e.call(t, http.MethodPost, "/v1/aliases/phone", token, map[string]any{"account_id": otherAccount, "phone": randomPhone()})
	if status != http.StatusNotFound && status != http.StatusForbidden {
		t.Errorf("número hacia una cuenta ajena: %d", status)
	}
}

func TestUnNumeroQueNoEsClienteLoDice(t *testing.T) {
	e := newPasskeyEnv(t)
	_, token, _, _ := e.linkedWithFunds(t)
	if status, out := e.call(t, http.MethodGet, "/v1/directory/phone/"+randomPhone(), token, nil); status != http.StatusNotFound || out["code"] != "phone_not_found" {
		t.Errorf("número ajeno: %d %v", status, out)
	}
	if status, out := e.call(t, http.MethodGet, "/v1/directory/phone/12345", token, nil); status != http.StatusBadRequest || out["code"] != "invalid_phone" {
		t.Errorf("número inválido: %d %v", status, out)
	}
}

// El destinatario de prueba trae celular, y el historial dice a quién se envió
// y de quién se recibió.
func TestEnviarAlCelularDelDestinatarioYVerloEnElHistorial(t *testing.T) {
	e := newPasskeyEnv(t)
	h, token, account, _ := e.linkedWithFunds(t)
	_, funds := e.call(t, http.MethodPost, "/v1/sandbox/funds", token, map[string]any{"account_id": account})
	phone := funds["payee_phone"].(string)

	_, found := e.call(t, http.MethodGet, "/v1/directory/phone/"+phone, token, nil)
	payee := found["account"].(map[string]any)
	if payee["id"] != funds["payee"].(map[string]any)["id"] || payee["name"] != "Juan P." {
		t.Fatalf("el celular de prueba lleva a %v", payee)
	}

	key := uuid.NewString()
	_, need := e.send(t, token, key, account, payee["id"].(string), 30_000_000, nil)
	if status, out := e.send(t, token, key, account, payee["id"].(string), 30_000_000, map[string]any{
		"challenge_id": need["challenge_id"], "method": "biometric", "signature": signB64(t, h.bio, need["payload"]),
	}); status != http.StatusCreated {
		t.Fatalf("enviar: %d %v", status, out)
	}

	_, home := e.call(t, http.MethodGet, "/v1/home?account_id="+account, token, nil)
	movements := home["movements"].([]any)
	sent := movements[0].(map[string]any)
	counterparty, _ := sent["counterparty"].(map[string]any)
	if counterparty["name"] != "Juan Pérez (prueba)" || sent["sign"] != float64(-1) {
		t.Errorf("el envío en el historial: %v", sent)
	}
	for _, m := range movements[1:] {
		if m.(map[string]any)["counterparty"] != nil {
			t.Errorf("un saldo de prueba expone la caja del banco: %v", m)
		}
	}
}
