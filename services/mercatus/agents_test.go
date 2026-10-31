package mercatus

import (
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"testing"

	"github.com/aibank/aibank/services/agents"
)

// El registro de agentes por la API de socio: alta, rotación de clave sin baja,
// retiro de la vieja y baja del agente (AE-10, AE-11).

func newAgentKey(t *testing.T) (agents.JWK, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return agents.ToJWK(pub), priv
}

func (f *fixture) registerAgent(t *testing.T, name string) (map[string]any, ed25519.PrivateKey) {
	t.Helper()
	jwk, priv := newAgentKey(t)
	status, out, _ := f.do(t, http.MethodPost, "/v1/agents", f.token,
		map[string]any{"name": name, "public_key": jwk}, nil)
	if status != http.StatusCreated {
		t.Fatalf("registrar agente: %d %v", status, out)
	}
	return out, priv
}

func TestUnaPlataformaRegistraAUnAgenteConSuClave(t *testing.T) {
	f := setup(t)
	agent, _ := f.registerAgent(t, "Agente de viajes")

	id, _ := agent["agent_id"].(string)
	keys, _ := agent["keys"].([]any)
	if mustDecode(t, agentPrefix, id) == "" || agent["name"] != "Agente de viajes" ||
		agent["status"] != "active" || len(keys) != 1 {
		t.Fatalf("agente: %v", agent)
	}
	status, got, _ := f.do(t, http.MethodGet, "/v1/agents/"+id, f.readOnly, nil, nil)
	if status != http.StatusOK || got["agent_id"] != id {
		t.Errorf("consultar: %d %v", status, got)
	}

	status, out, _ := f.do(t, http.MethodPost, "/v1/agents", f.token,
		map[string]any{"name": "Sin clave", "public_key": map[string]string{"kty": "RSA"}}, nil)
	if status != http.StatusBadRequest || out["error"] != CodeMalformedRequest {
		t.Errorf("clave que no es Ed25519: %d %v", status, out)
	}
}

func TestRotarLaClaveNoDaDeBajaAlAgente(t *testing.T) {
	f := setup(t)
	agent, _ := f.registerAgent(t, "Agente")
	id := agent["agent_id"].(string)
	old := agent["keys"].([]any)[0].(map[string]any)["key_id"].(string)

	// La única clave no se puede retirar.
	status, out, _ := f.do(t, http.MethodDelete, "/v1/agents/"+id+"/keys/"+old, f.token, nil, nil)
	if status != http.StatusConflict || out["error"] != CodeAgentLastKey {
		t.Fatalf("retirar la única: %d %v", status, out)
	}

	jwk, _ := newAgentKey(t)
	status, added, _ := f.do(t, http.MethodPost, "/v1/agents/"+id+"/keys", f.token,
		map[string]any{"public_key": jwk}, nil)
	if status != http.StatusCreated || added["expires_at"] != nil {
		t.Fatalf("añadir clave: %d %v", status, added)
	}
	_, got, _ := f.do(t, http.MethodGet, "/v1/agents/"+id, f.token, nil, nil)
	keys := got["keys"].([]any)
	if len(keys) != 2 || keys[0].(map[string]any)["expires_at"] == nil {
		t.Errorf("la vieja tiene que quedar con vencimiento: %v", keys)
	}

	status, _, _ = f.do(t, http.MethodDelete, "/v1/agents/"+id+"/keys/"+old, f.token, nil, nil)
	if status != http.StatusOK {
		t.Errorf("retirar la vieja: %d", status)
	}
}

func TestUnAgenteDadoDeBajaNoAdmiteClavesNuevas(t *testing.T) {
	f := setup(t)
	agent, _ := f.registerAgent(t, "Agente")
	id := agent["agent_id"].(string)

	status, got, _ := f.do(t, http.MethodDelete, "/v1/agents/"+id, f.token, nil, nil)
	if status != http.StatusOK || got["status"] != "revoked" {
		t.Fatalf("dar de baja: %d %v", status, got)
	}
	jwk, _ := newAgentKey(t)
	status, out, _ := f.do(t, http.MethodPost, "/v1/agents/"+id+"/keys", f.token,
		map[string]any{"public_key": jwk}, nil)
	if status != http.StatusConflict || out["error"] != CodeAgentRevoked {
		t.Errorf("clave nueva tras la baja: %d %v", status, out)
	}
}
