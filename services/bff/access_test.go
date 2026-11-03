package bff_test

import (
	"net/http"
	"testing"
)

// El centro de permisos (F3-06) por HTTP: todo lo que tiene acceso a la cuenta,
// con nombres que la persona reconoce, y quitarlo sin quedarse fuera.

func (e *passkeyEnv) access(t *testing.T, token string) map[string]any {
	t.Helper()
	status, out := e.call(t, http.MethodGet, "/v1/access", token, nil)
	if status != http.StatusOK {
		t.Fatalf("centro de permisos: %d %v", status, out)
	}
	return out
}

func TestElCentroDePermisosMuestraTodoLoQueTieneAcceso(t *testing.T) {
	e := newPasskeyEnv(t)
	h, token, _, account := e.linkedHolder(t)
	handoff := e.platformAsks(t, token)
	_, need := e.grant(t, token, handoff, account, 10_000_000, nil)
	if status, out := e.grant(t, token, handoff, account, 10_000_000, map[string]any{
		"challenge_id": need["challenge_id"], "method": "biometric", "signature": signB64(t, h.bio, need["payload"]),
	}); status != http.StatusCreated {
		t.Fatalf("dar permiso: %d %v", status, out)
	}

	a := e.access(t, token)
	platforms := a["platforms"].([]any)
	if len(platforms) != 1 || platforms[0].(map[string]any)["agent_name"] != "Agente de compras (prueba)" {
		t.Errorf("plataformas: %v", platforms)
	}
	phones := a["phones"].([]any)
	if len(phones) != 1 || phones[0].(map[string]any)["current"] != true || phones[0].(map[string]any)["name"] != "iPhone de prueba" {
		t.Errorf("teléfonos: %v", phones)
	}
	if len(a["passkeys"].([]any)) != 0 {
		t.Errorf("passkeys: %v", a["passkeys"])
	}
	// La actividad dice quién, con nombre: la persona desde su teléfono.
	var granted bool
	for _, item := range a["activity"].([]any) {
		act := item.(map[string]any)
		if act["action"] == "grant_permission" && act["outcome"] == "confirmed" {
			granted = act["actor_name"] == "Tú, desde iPhone de prueba"
		}
	}
	if !granted {
		t.Errorf("actividad: %v", a["activity"])
	}
}

func TestQuitarUnaPasskeySinQuedarseFuera(t *testing.T) {
	e := newPasskeyEnv(t)
	token, _, _, _ := e.onboard(t) // entra con una passkey
	if status, out := e.link(t, newHandset(), "", token, "482916"); status != http.StatusCreated {
		t.Fatalf("vincular teléfono: %d %v", status, out)
	}
	a := e.access(t, token)
	passkeys := a["passkeys"].([]any)
	phones := a["phones"].([]any)
	if len(passkeys) != 1 || len(phones) != 1 || passkeys[0].(map[string]any)["current"] != true {
		t.Fatalf("formas de entrar: %v / %v", passkeys, phones)
	}

	// Otra persona no la puede quitar.
	otherToken, _, _, _ := e.onboard(t)
	e.link(t, newHandset(), "", otherToken, "482916")
	passkey := passkeys[0].(map[string]any)["id"].(string)
	if status, _ := e.call(t, http.MethodDelete, "/v1/passkeys/"+passkey, otherToken, nil); status != http.StatusNotFound {
		t.Errorf("quitar la passkey de otro: %d", status)
	}

	if status, out := e.call(t, http.MethodDelete, "/v1/passkeys/"+passkey, token, nil); status != http.StatusNoContent {
		t.Fatalf("quitar la passkey: %d %v", status, out)
	}
	// La sesión que se abrió con ella se cerró.
	if status, _ := e.call(t, http.MethodGet, "/v1/access", token, nil); status != http.StatusUnauthorized {
		t.Errorf("la sesión de la passkey quitada sigue: %d", status)
	}
}

func TestLaUltimaFormaDeEntrarNoSeQuita(t *testing.T) {
	e := newPasskeyEnv(t)
	token, _, _, _ := e.onboard(t)
	passkey := e.access(t, token)["passkeys"].([]any)[0].(map[string]any)["id"].(string)
	status, out := e.call(t, http.MethodDelete, "/v1/passkeys/"+passkey, token, nil)
	if status != http.StatusConflict || out["code"] != "last_way_in" {
		t.Errorf("quitar la única passkey: %d %v", status, out)
	}
}
