package bff_test

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
)

// La tarjeta de crédito por HTTP: pedirla, usarla hacia la cuenta propia y pagarla.

func (e *passkeyEnv) creditMove(t *testing.T, path, token, account string, amount int64) (int, map[string]any) {
	t.Helper()
	return e.callWith(t, http.MethodPost, path, token, map[string]string{"Idempotency-Key": uuid.NewString()},
		map[string]any{"account_id": account, "amount_micros": amount})
}

func TestPedirLaTarjetaUsarlaYPagarla(t *testing.T) {
	e := newPasskeyEnv(t)
	_, token, account, _ := e.linkedWithFunds(t)

	if status, out := e.call(t, http.MethodGet, "/v1/credit/card", token, nil); status != http.StatusNotFound || out["code"] != "no_credit_card" {
		t.Fatalf("antes de pedirla: %d %v", status, out)
	}
	status, decision := e.call(t, http.MethodPost, "/v1/credit/applications", token, map[string]any{"account_id": account})
	if status != http.StatusOK || decision["outcome"] != "approved" {
		t.Fatalf("solicitud: %d %v", status, decision)
	}
	card := decision["card"].(map[string]any)
	if card["limit"].(map[string]any)["amount_micros"] != float64(500_000_000) || card["annual_rate_bps"] != float64(4500) {
		t.Errorf("tarjeta = %v", card)
	}

	// 200 de la tarjeta a la cuenta (que tiene 250 y tope de 1.000).
	if status, out := e.creditMove(t, "/v1/credit/card/draws", token, account, 200_000_000); status != http.StatusCreated {
		t.Fatalf("disposición: %d %v", status, out)
	}
	if got := e.balance(t, token, account); got != 450_000_000 {
		t.Errorf("saldo tras disponer = %v", got)
	}
	_, card = e.call(t, http.MethodGet, "/v1/credit/card", token, nil)
	if card["outstanding"].(map[string]any)["amount_micros"] != float64(200_000_000) {
		t.Errorf("deuda = %v", card["outstanding"])
	}

	if status, out := e.creditMove(t, "/v1/credit/card/payments", token, account, 50_000_000); status != http.StatusCreated {
		t.Fatalf("pago: %d %v", status, out)
	}
	if status, out := e.creditMove(t, "/v1/credit/card/payments", token, account, 150_000_001); status != http.StatusUnprocessableEntity || out["code"] != "overpayment" {
		t.Errorf("pagar de más: %d %v", status, out)
	}
	// Quedan 350 disponibles. La cuenta de nivel 1 acepta hasta 300 por operación,
	// así que se dispone en dos veces: 250 entra; 150 más ya supera la tarjeta.
	if status, out := e.creditMove(t, "/v1/credit/card/draws", token, account, 250_000_000); status != http.StatusCreated {
		t.Fatalf("segunda disposición: %d %v", status, out)
	}
	if status, out := e.creditMove(t, "/v1/credit/card/draws", token, account, 150_000_000); status != http.StatusUnprocessableEntity || out["code"] != "credit_limit_exceeded" {
		t.Errorf("por encima de lo disponible: %d %v", status, out)
	}
	if status, out := e.call(t, http.MethodPost, "/v1/credit/applications", token, map[string]any{"account_id": account}); status != http.StatusConflict || out["code"] != "already_has_card" {
		t.Errorf("segunda solicitud: %d %v", status, out)
	}
}

// El dinero de la tarjeta solo va a una cuenta propia, y solo se paga desde una.
func TestLaTarjetaSoloSeMueveConCuentasPropias(t *testing.T) {
	e := newPasskeyEnv(t)
	_, token, account, _ := e.linkedWithFunds(t)
	_, _, otherAccount, _ := e.linkedWithFunds(t)
	e.call(t, http.MethodPost, "/v1/credit/applications", token, map[string]any{"account_id": account})

	if status, _ := e.creditMove(t, "/v1/credit/card/draws", token, otherAccount, 10_000_000); status != http.StatusNotFound {
		t.Errorf("disponer hacia una cuenta ajena: %d", status)
	}
	if status, _ := e.creditMove(t, "/v1/credit/card/payments", token, otherAccount, 1_000_000); status != http.StatusNotFound {
		t.Errorf("pagar desde una cuenta ajena: %d", status)
	}
	if status, _ := e.call(t, http.MethodPost, "/v1/credit/applications", token, map[string]any{"account_id": otherAccount}); status != http.StatusNotFound {
		t.Errorf("pedir la tarjeta evaluando una cuenta ajena: %d", status)
	}
}

// El ciclo por HTTP, con los atajos del sandbox: cortar, vencer y ponerse al día.
func TestElEstadoDeCuentaYLaMoraPorHTTP(t *testing.T) {
	e := newPasskeyEnv(t)
	_, token, account, _ := e.linkedWithFunds(t)
	e.call(t, http.MethodPost, "/v1/credit/applications", token, map[string]any{"account_id": account})
	e.creditMove(t, "/v1/credit/card/draws", token, account, 200_000_000)

	status, st := e.call(t, http.MethodPost, "/v1/sandbox/credit/close-cycle", token, nil)
	if status != http.StatusOK || st["status"] != "pending" {
		t.Fatalf("corte: %d %v", status, st)
	}
	if st["minimum_payment"].(map[string]any)["amount_micros"] != float64(10_000_000) {
		t.Errorf("mínimo = %v", st["minimum_payment"])
	}
	_, card := e.call(t, http.MethodGet, "/v1/credit/card", token, nil)
	if card["statement"].(map[string]any)["closing"].(map[string]any)["amount_micros"] != float64(200_000_000) {
		t.Errorf("la tarjeta no trae su estado de cuenta: %v", card["statement"])
	}

	status, line := e.call(t, http.MethodPost, "/v1/sandbox/credit/pass-due-date", token, nil)
	if status != http.StatusOK || line["status"] != "blocked" {
		t.Fatalf("tras el vencimiento sin pagar: %d %v", status, line)
	}
	if status, out := e.creditMove(t, "/v1/credit/card/draws", token, account, 1_000_000); status != http.StatusUnprocessableEntity || out["code"] != "credit_line_inactive" {
		t.Errorf("usar bloqueada: %d %v", status, out)
	}

	e.creditMove(t, "/v1/credit/card/payments", token, account, 10_000_000)
	_, card = e.call(t, http.MethodGet, "/v1/credit/card", token, nil)
	if card["status"] != "active" || card["statement"].(map[string]any)["status"] != "minimum_paid" {
		t.Errorf("tras pagar el mínimo: %v / %v", card["status"], card["statement"])
	}
	_, list := e.call(t, http.MethodGet, "/v1/credit/card/statements", token, nil)
	if len(list["statements"].([]any)) != 1 {
		t.Errorf("estados de cuenta = %v", list)
	}
}
