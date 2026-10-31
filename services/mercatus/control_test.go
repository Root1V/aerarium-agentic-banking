package mercatus

import (
	"net/http"
	"testing"

	"github.com/aibank/aibank/services/control"
	"github.com/google/uuid"
)

// Los pagos de los agentes pasan por la misma capa de control que lo que hace el
// titular desde su app, y quedan en su registro de decisiones: quién pagó, bajo
// qué permiso, y por qué se permitió o no.

func (f *modelBFixture) payUnderMandate(t *testing.T, mandateID string, amount int64) (int, map[string]any) {
	t.Helper()
	status, out, _ := f.do(t, http.MethodPost, "/v1/authorizations", f.token, map[string]any{
		"mandate_id":       mandateID,
		"payer_account_id": encodeID(accountPrefix, f.agentAcct),
		"payee_account_id": f.payee,
		"amount":           amount,
		"currency":         "USD",
	}, map[string]string{"Idempotency-Key": "cart-" + uuid.NewString()})
	return status, out
}

// agentDecisions son las decisiones sobre pagos de agentes en el registro del
// titular, de la más antigua a la más reciente.
func (f *modelBFixture) agentDecisions(t *testing.T) []control.Entry {
	t.Helper()
	history, err := f.control.History(f.ctx, f.holderID, 50)
	if err != nil {
		t.Fatal(err)
	}
	var out []control.Entry
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Action == control.ActionMandatePayment {
			out = append(out, history[i])
		}
	}
	return out
}

func TestLosPagosDelAgenteQuedanEnElRegistroDelTitular(t *testing.T) {
	f := setupModelB(t)
	mandateID := f.grantMandate(t, i64(5_000000), i64(20_000000))

	if status, out := f.payUnderMandate(t, mandateID, 3_000000); status != http.StatusCreated {
		t.Fatalf("pago dentro del permiso: %d %v", status, out)
	}
	status, out := f.payUnderMandate(t, mandateID, 9_000000)
	if status != http.StatusUnprocessableEntity || out["error"] != CodeMandateLimitExceeded {
		t.Fatalf("pago sobre el tope: %d %v", status, out)
	}

	decisions := f.agentDecisions(t)
	if len(decisions) != 2 {
		t.Fatalf("registro: %+v", decisions)
	}
	allowed, denied := decisions[0], decisions[1]
	// El actor es el agente que firmó el pago, no la plataforma.
	if allowed.Outcome != "allow" || allowed.Actor != (control.Actor{Kind: control.ActorAgent, ID: mustDecode(t, agentPrefix, f.agentID)}) ||
		allowed.Level != control.LevelMandate || allowed.Evidence != "grant:mandate:"+mandateID {
		t.Errorf("pago permitido: %+v", allowed)
	}
	if denied.Outcome != "deny" || denied.Reason != control.ReasonOverLimit {
		t.Errorf("pago sobre el tope: %+v", denied)
	}
}

// El tope total también se decide en la capa de control, y con el mismo código
// que daría el core: el contrato no cambia.
func TestLoQueExcedeElTopeTotalSeNiegaYQuedaEscrito(t *testing.T) {
	f := setupModelB(t)
	mandateID := f.grantMandate(t, i64(5_000000), i64(6_000000))

	if status, out := f.payUnderMandate(t, mandateID, 4_000000); status != http.StatusCreated {
		t.Fatalf("primer pago: %d %v", status, out)
	}
	status, out := f.payUnderMandate(t, mandateID, 4_000000)
	if status != http.StatusUnprocessableEntity || out["error"] != CodeMandateLimitExceeded {
		t.Fatalf("segundo pago: %d %v", status, out)
	}
	decisions := f.agentDecisions(t)
	if last := decisions[len(decisions)-1]; last.Outcome != "deny" || last.Reason != control.ReasonOverLimit {
		t.Errorf("registro: %+v", last)
	}
}

func TestUnPermisoRetiradoSeNiegaYQuedaEscrito(t *testing.T) {
	f := setupModelB(t)
	mandateID := f.grantMandate(t, nil, nil)
	if status, _ := f.callBFF(t, http.MethodPost, "/v1/mandates/"+mandateID+"/revoke", f.holderToken, nil); status != http.StatusOK {
		t.Fatalf("revocar: %d", status)
	}

	status, out := f.payUnderMandate(t, mandateID, 1_000000)
	if status != http.StatusForbidden || out["error"] != CodeMandateRevoked {
		t.Fatalf("pago con el permiso retirado: %d %v", status, out)
	}
	decisions := f.agentDecisions(t)
	if len(decisions) != 1 || decisions[0].Outcome != "deny" || decisions[0].Reason != control.ReasonGrantInactive {
		t.Errorf("registro: %+v", decisions)
	}
}
