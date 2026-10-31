package mercatus

import (
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"strings"
	"testing"

	"github.com/aibank/aibank/services/agents"
	"github.com/google/uuid"
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

// signAs hace que los pagos de la prueba los firme un agente nuevo de otra
// integración. Devuelve con qué volver al agente de siempre.
func (f *fixture) signAs(t *testing.T, clientID string) func() {
	t.Helper()
	jwk, pk := newAgentKey(t)
	agent, err := f.agents.Register(f.ctx, clientID, "Agente ajeno", jwk)
	if err != nil {
		t.Fatalf("registrar agente ajeno: %v", err)
	}
	id, key, prev := f.agentID, f.agentKey, f.agentPK
	f.agentID, f.agentKey, f.agentPK = encodeID(agentPrefix, agent.ID), encodeID(agentKeyPrefix, agent.Keys[0].ID), pk
	return func() { f.agentID, f.agentKey, f.agentPK = id, key, prev }
}

// ---------------------------------------------------------------- en cada pago

func (f *fixture) pay(t *testing.T, body map[string]any, headers map[string]string) (int, map[string]any) {
	t.Helper()
	base := map[string]any{"payer_account_id": f.payer, "payee_account_id": f.payee, "amount": 1_000000, "currency": "USD"}
	for k, v := range body {
		base[k] = v
	}
	h := map[string]string{"Idempotency-Key": "cart-" + uuid.NewString()}
	for k, v := range headers {
		h[k] = v
	}
	status, out, _ := f.do(t, http.MethodPost, "/v1/authorizations", f.token, base, h)
	return status, out
}

func TestUnPagoSinFirmaDelAgenteNoPasa(t *testing.T) {
	f := setup(t)
	status, out := f.pay(t, nil, map[string]string{"Unsigned": "true"})
	if status != http.StatusUnauthorized || out["error"] != CodeAgentSignatureInvalid {
		t.Errorf("sin firma: %d %v", status, out)
	}
}

// Firmar como un agente y decir en el cuerpo que paga otro es lo mismo que no
// firmar.
func TestElAgenteDelCuerpoTieneQueSerElQueFirma(t *testing.T) {
	f := setup(t)
	other, _ := f.registerAgent(t, "Otro agente")
	status, out := f.pay(t, map[string]any{"agent_id": other["agent_id"]}, nil)
	if status != http.StatusUnauthorized || out["error"] != CodeAgentSignatureInvalid {
		t.Errorf("agent_id de otro: %d %v", status, out)
	}
}

func TestSinIntencionNoSePaga(t *testing.T) {
	f := setup(t)
	for name, intent := range map[string]any{
		"sin intent":        nil,
		"sin reference":     map[string]any{"description": "algo"},
		"descripción larga": map[string]any{"reference": "t1", "description": strings.Repeat("x", 141)},
		"con control":       map[string]any{"reference": "t1", "description": "línea\nnueva"},
	} {
		status, out := f.pay(t, map[string]any{"intent": intent}, nil)
		if status != http.StatusBadRequest || out["error"] != CodeMalformedRequest {
			t.Errorf("%s: %d %v", name, status, out)
		}
	}
}

func TestLaIntencionQuedaGuardadaConElPago(t *testing.T) {
	f := setup(t)
	status, auth := f.pay(t, map[string]any{"intent": map[string]any{
		"reference": "conversacion-42", "description": "Text Summarizer: resumen de un texto en 2 oraciones"}}, nil)
	if status != http.StatusCreated {
		t.Fatalf("pagar: %d %v", status, auth)
	}
	id := mustDecode(t, authorizationPrefix, auth["authorization_id"].(string))
	intents, err := f.agents.IntentsFor(f.ctx, []string{id})
	if err != nil {
		t.Fatal(err)
	}
	got := intents[id]
	if got.Reference != "conversacion-42" || got.AgentName != "Agente de pruebas" ||
		got.Description != "Text Summarizer: resumen de un texto en 2 oraciones" {
		t.Errorf("intención guardada: %+v", got)
	}
}

// Modelo A: la sub-cuenta pagadora es de un agente, y solo él paga con ella.
func TestUnaCuentaSoloPagaConSuAgente(t *testing.T) {
	f := setup(t)
	defer f.signAs(t, f.clientID)()
	status, out := f.pay(t, nil, nil)
	if status != http.StatusForbidden || out["error"] != CodeAccountAgentScope {
		t.Errorf("otro agente con la cuenta: %d %v", status, out)
	}
}

func TestUnaCuentaNoCambiaDeAgente(t *testing.T) {
	f := setup(t)
	other, _ := f.registerAgent(t, "Otro")
	status, out, _ := f.do(t, http.MethodPost, "/v1/accounts", f.token, map[string]any{
		"owner_reference": "otra_" + uuid.NewString(), "currency": "USD", "agent_id": f.agentID}, nil)
	if status != http.StatusCreated {
		t.Fatalf("abrir: %d %v", status, out)
	}
	ref := "fija_" + uuid.NewString()
	f.do(t, http.MethodPost, "/v1/accounts", f.token, map[string]any{"owner_reference": ref, "currency": "USD", "agent_id": f.agentID}, nil)
	status, out, _ = f.do(t, http.MethodPost, "/v1/accounts", f.token, map[string]any{
		"owner_reference": ref, "currency": "USD", "agent_id": other["agent_id"]}, nil)
	if status != http.StatusConflict || out["error"] != CodeAccountAgentConflict {
		t.Errorf("reabrir para otro agente: %d %v", status, out)
	}
}

func TestUnAgenteDadoDeBajaYaNoPaga(t *testing.T) {
	f := setup(t)
	if status, _, _ := f.do(t, http.MethodDelete, "/v1/agents/"+f.agentID, f.token, nil, nil); status != http.StatusOK {
		t.Fatalf("dar de baja: %d", status)
	}
	status, out := f.pay(t, nil, nil)
	if status != http.StatusUnauthorized || out["error"] != CodeAgentSignatureInvalid {
		t.Errorf("pagar tras la baja: %d %v", status, out)
	}
}

// Durante la rotación pagan las dos claves.
func TestDuranteLaRotacionFirmanLasDosClaves(t *testing.T) {
	f := setup(t)
	jwk, pk := newAgentKey(t)
	status, added, _ := f.do(t, http.MethodPost, "/v1/agents/"+f.agentID+"/keys", f.token, map[string]any{"public_key": jwk}, nil)
	if status != http.StatusCreated {
		t.Fatalf("añadir clave: %d %v", status, added)
	}
	if status, out := f.pay(t, nil, nil); status != http.StatusCreated {
		t.Errorf("con la vieja: %d %v", status, out)
	}
	f.agentKey, f.agentPK = added["key_id"].(string), pk
	if status, out := f.pay(t, nil, nil); status != http.StatusCreated {
		t.Errorf("con la nueva: %d %v", status, out)
	}
}
