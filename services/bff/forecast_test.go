package bff_test

import (
	"net/http"
	"testing"
	"time"
)

// La previsión de saldo por HTTP: con la tarjeta y su estado de cuenta, el
// saldo de hoy y el sueldo que espera el motor.
func TestLaPrevisionCuentaElPagoDeLaTarjetaAntesDelSueldo(t *testing.T) {
	e := newPasskeyEnv(t)
	_, token, account, _ := e.linkedWithFunds(t)
	e.call(t, http.MethodPost, "/v1/credit/applications", token, map[string]any{"account_id": account})
	if status, out := e.creditMove(t, "/v1/credit/card/draws", token, account, 200_000_000); status != http.StatusCreated {
		t.Fatalf("disposición: %d %v", status, out)
	}
	// El corte: vence en 20 días, antes del sueldo (en 25).
	if status, st := e.call(t, http.MethodPost, "/v1/sandbox/credit/close-cycle", token, nil); status != http.StatusOK {
		t.Fatalf("corte: %d %v", status, st)
	}

	status, f := e.call(t, http.MethodGet, "/v1/accounts/"+account+"/forecast", token, nil)
	if status != http.StatusOK {
		t.Fatalf("previsión: %d %v", status, f)
	}
	income := f["income"].(map[string]any)
	if income["expected_on"] != time.Now().AddDate(0, 0, 25).Format(time.DateOnly) || f["until"] != income["expected_on"] {
		t.Errorf("hasta el sueldo: %v / %v", f["until"], income)
	}
	card := f["card"].(map[string]any)
	if card["total"].(map[string]any)["amount_micros"] != float64(200_000_000) {
		t.Errorf("tarjeta: %v", card)
	}
	// 450 de saldo, sin gasto observado, y 200 al vencer la tarjeta.
	after := f["after_card"].(map[string]any)
	if after["on"] != card["due_on"] || after["amount"].(map[string]any)["amount_micros"] != float64(250_000_000) {
		t.Errorf("al pagar la tarjeta: %v", after)
	}
	if f["status"] != "ok" {
		t.Errorf("estado: %v", f["status"])
	}
}

func TestLaPrevisionDeUnaCuentaAjenaNoExiste(t *testing.T) {
	e := newPasskeyEnv(t)
	_, token, _, _ := e.linkedWithFunds(t)
	_, _, other, _ := e.linkedWithFunds(t)
	if status, _ := e.call(t, http.MethodGet, "/v1/accounts/"+other+"/forecast", token, nil); status != http.StatusNotFound {
		t.Errorf("previsión de otra cuenta: %d", status)
	}
}
