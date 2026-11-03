package bff_test

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
)

// El detalle de una transferencia dice cómo se autorizó: con la cara y por qué
// se pidió, o sin pedirla y por qué no hizo falta.
func TestElDetalleDiceComoSeAutorizoCadaEnvio(t *testing.T) {
	e := newPasskeyEnv(t)
	h, token, account, payee := e.linkedWithFunds(t)

	key := uuid.NewString()
	_, need := e.send(t, token, key, account, payee, 50_000_000, nil)
	status, sent := e.send(t, token, key, account, payee, 50_000_000, map[string]any{
		"challenge_id": need["challenge_id"], "method": "biometric", "signature": signB64(t, h.bio, need["payload"]),
	})
	if status != http.StatusCreated {
		t.Fatalf("enviar con la cara: %d %v", status, sent)
	}
	detail := func(tx string) map[string]any {
		t.Helper()
		status, out := e.call(t, http.MethodGet, "/v1/accounts/"+account+"/movements/"+tx, token, nil)
		if status != http.StatusOK {
			t.Fatalf("detalle: %d %v", status, out)
		}
		return out
	}

	first := detail(sent["transaction_id"].(string))["authorization"].(map[string]any)
	if first["outcome"] != "confirmed" || first["method"] != "biometric" || first["reason"] != "new_payee" ||
		first["device"] != "iPhone de prueba" {
		t.Errorf("con la cara: %v", first)
	}

	// La segunda vez, conocido y poco: sin pedir la cara, y dice por qué.
	_, again := e.send(t, token, uuid.NewString(), account, payee, 10_000_000, nil)
	second := detail(again["transaction_id"].(string))["authorization"].(map[string]any)
	if second["outcome"] != "allow" || second["reason"] != "known_payee" {
		t.Errorf("sin confirmar: %v", second)
	}

	// Un movimiento de otra cuenta no tiene detalle para quien pregunta.
	_, otherToken, _, _ := e.linkedWithFunds(t)
	if status, _ := e.call(t, http.MethodGet, "/v1/accounts/"+account+"/movements/"+again["transaction_id"].(string), otherToken, nil); status != http.StatusNotFound {
		t.Errorf("detalle de una cuenta ajena: %d", status)
	}
}
