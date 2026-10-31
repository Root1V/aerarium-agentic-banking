package bff_test

import (
	"net/http"
	"testing"

	"github.com/aibank/aibank/services/control"
)

// La capa de control por HTTP: darle a una plataforma permiso para pagar es de
// alto impacto y se confirma siempre con el teléfono, y cada decisión queda
// registrada.

// linkedHolder da de alta a alguien y vincula su teléfono.
func (e *passkeyEnv) linkedHolder(t *testing.T) (h *handset, token, customer, account string) {
	t.Helper()
	reference, customer, account := e.approve(t)
	h = newHandset()
	status, linked := e.link(t, h, reference, "", "482916")
	if status != http.StatusCreated {
		t.Fatalf("vincular: %d %v", status, linked)
	}
	return h, linked["token"].(string), customer, account
}

// platformAsks hace que la plataforma de prueba pida permiso y devuelve el
// código con el que la app encuentra la solicitud.
func (e *passkeyEnv) platformAsks(t *testing.T, token string) string {
	t.Helper()
	status, out := e.call(t, http.MethodPost, "/v1/sandbox/consent-request", token, map[string]any{"currency": "USD"})
	if status != http.StatusCreated {
		t.Fatalf("solicitud de prueba: %d %v", status, out)
	}
	return out["handoff_code"].(string)
}

func (e *passkeyEnv) grant(t *testing.T, token, handoff, account string, perOperation int64, confirmation map[string]any) (int, map[string]any) {
	t.Helper()
	body := map[string]any{"account_id": account, "max_per_operation": perOperation,
		"max_total": 100_000_000, "expires_in_days": 30}
	if confirmation != nil {
		body["confirmation"] = confirmation
	}
	return e.call(t, http.MethodPost, "/v1/consent-requests/"+handoff+"/approve", token, body)
}

func (e *passkeyEnv) mandateCount(t *testing.T, token string) int {
	t.Helper()
	status, out := e.call(t, http.MethodGet, "/v1/mandates", token, nil)
	if status != http.StatusOK {
		t.Fatalf("permisos: %d %v", status, out)
	}
	return len(out["mandates"].([]any))
}

func TestDarUnPermisoDePagoPideLaCaraSiempre(t *testing.T) {
	e := newPasskeyEnv(t)
	h, token, customer, account := e.linkedHolder(t)
	handoff := e.platformAsks(t, token)

	// Sin confirmar no se concede nada, aunque los topes sean chicos.
	status, need := e.grant(t, token, handoff, account, 10_000_000, nil)
	if status != http.StatusPreconditionRequired || need["reason"] != "high_impact" {
		t.Fatalf("conceder sin confirmar: %d %v", status, need)
	}
	summary := need["summary"].(map[string]any)
	if summary["kind"] != "grant_permission" || summary["to_name"] == "" ||
		summary["agent_name"] != "Agente de compras (prueba)" ||
		summary["max_per_operation"] != float64(10_000_000) || summary["expires_in_days"] != float64(30) {
		t.Errorf("lo que se le muestra a la persona: %v", summary)
	}
	if n := e.mandateCount(t, token); n != 0 {
		t.Fatalf("pedir confirmación concedió el permiso: %d permisos", n)
	}

	status, mandate := e.grant(t, token, handoff, account, 10_000_000, map[string]any{
		"challenge_id": need["challenge_id"], "method": "biometric",
		"signature": signB64(t, h.bio, need["payload"]),
	})
	if status != http.StatusCreated || mandate["status"] != "active" {
		t.Fatalf("conceder confirmado: %d %v", status, mandate)
	}
	if n := e.mandateCount(t, token); n != 1 {
		t.Errorf("permisos tras conceder: %d", n)
	}

	// Queda la decisión y la confirmación, con el teléfono que firmó.
	history, err := control.NewService(e.db, nil).History(e.f.ctx, customer, 10)
	if err != nil {
		t.Fatal(err)
	}
	var asked, confirmed bool
	for _, d := range history {
		if d.Action != control.ActionGrantPermission {
			continue
		}
		asked = asked || (d.Outcome == "confirm" && d.Level == control.LevelHuman)
		confirmed = confirmed || (d.Outcome == "confirmed" && d.Evidence == "device:"+h.id+";method:biometric")
	}
	if !asked || !confirmed {
		t.Errorf("registro de decisiones: %+v", history)
	}
}

// La firma de un permiso de 10 por pago no sirve para conceder uno de 20.
func TestLaFirmaDeUnPermisoNoSirveParaOtrosTopes(t *testing.T) {
	e := newPasskeyEnv(t)
	h, token, _, account := e.linkedHolder(t)
	handoff := e.platformAsks(t, token)

	_, need := e.grant(t, token, handoff, account, 10_000_000, nil)
	status, out := e.grant(t, token, handoff, account, 20_000_000, map[string]any{
		"challenge_id": need["challenge_id"], "method": "biometric",
		"signature": signB64(t, h.bio, need["payload"]),
	})
	if status != http.StatusForbidden || out["code"] != "confirmation_rejected" {
		t.Errorf("topes cambiados: %d %v", status, out)
	}
	if n := e.mandateCount(t, token); n != 0 {
		t.Errorf("se concedió con una firma ajena: %d permisos", n)
	}
}

// Las transferencias también quedan en el registro, permitidas o no.
func TestLasTransferenciasQuedanRegistradas(t *testing.T) {
	e := newPasskeyEnv(t)
	_, token, customer, account := e.linkedHolder(t)
	status, funds := e.call(t, http.MethodPost, "/v1/sandbox/funds", token, map[string]any{"account_id": account})
	if status != http.StatusOK {
		t.Fatalf("saldo de prueba: %d %v", status, funds)
	}
	payee := funds["payee"].(map[string]any)["id"].(string)
	if status, _ := e.send(t, token, "k-1", account, payee, 10_000_000, nil); status != http.StatusPreconditionRequired {
		t.Fatalf("primer envío: %d", status)
	}
	history, err := control.NewService(e.db, nil).History(e.f.ctx, customer, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].Action != control.ActionTransfer || history[0].Reason != control.ReasonNewPayee ||
		history[0].Actor.Kind != control.ActorHolder {
		t.Errorf("registro: %+v", history)
	}
}
