package bff_test

import (
	"net/http"
	"testing"
	"time"
)

// Cuentas en soles y dólares, y cambiar entre las propias con el costo dicho.

func (e *passkeyEnv) openAccount(t *testing.T, token, currency string) string {
	t.Helper()
	status, out := e.call(t, http.MethodPost, "/v1/accounts", token, map[string]any{"currency": currency})
	if status != http.StatusCreated {
		t.Fatalf("abrir cuenta en %s: %d %v", currency, status, out)
	}
	return out["id"].(string)
}

func TestUnaCuentaEnCadaMoneda(t *testing.T) {
	e := newPasskeyEnv(t)
	_, token, account, _ := e.linkedWithFunds(t)
	_, home := e.call(t, http.MethodGet, "/v1/home?account_id="+account, token, nil)
	first := home["balance"].(map[string]any)["currency"].(string)
	other := "PEN"
	if first == "PEN" {
		other = "USD"
	}
	second := e.openAccount(t, token, other)
	if status, out := e.call(t, http.MethodPost, "/v1/accounts", token, map[string]any{"currency": other}); status != http.StatusConflict || out["code"] != "account_exists" {
		t.Errorf("segunda cuenta en la misma moneda: %d %v", status, out)
	}
	if status, _ := e.call(t, http.MethodPost, "/v1/accounts", token, map[string]any{"currency": "EUR"}); status != http.StatusBadRequest {
		t.Errorf("moneda no disponible: %d", status)
	}
	_, list := e.call(t, http.MethodGet, "/v1/accounts", token, nil)
	if len(list["accounts"].([]any)) != 2 {
		t.Errorf("cuentas: %v (la nueva %s)", list, second)
	}
}

func TestCambiarDolaresASolesConElCostoDicho(t *testing.T) {
	e := newPasskeyEnv(t)
	_, token, usd, _ := e.linkedWithFunds(t) // 250 USD
	pen := e.openAccount(t, token, "PEN")

	status, q := e.call(t, http.MethodPost, "/v1/fx/quotes", token, map[string]any{
		"from_account_id": usd, "to_account_id": pen, "amount_micros": 100_000_000})
	if status != http.StatusCreated {
		t.Fatalf("cotizar: %d %v", status, q)
	}
	// Referencia 3,38; el banco compra al 0,5 % menos: 3,3631. 100 USD dan
	// S/ 336,31, y frente a la referencia (S/ 338,00) el cambio cuesta S/ 1,69.
	if q["applied_rate"] != float64(3_363_100) || q["to"].(map[string]any)["amount_micros"] != float64(336_310_000) ||
		q["cost"].(map[string]any)["amount_micros"] != float64(1_690_000) {
		t.Fatalf("cotización: %v", q)
	}

	status, done := e.call(t, http.MethodPost, "/v1/fx/conversions", token, map[string]any{"quote_id": q["quote_id"]})
	if status != http.StatusCreated {
		t.Fatalf("cambiar: %d %v", status, done)
	}
	if got := e.balance(t, token, usd); got != 150_000_000 {
		t.Errorf("dólares después: %v", got)
	}
	if got := e.balance(t, token, pen); got != 336_310_000 {
		t.Errorf("soles después: %v", got)
	}
	// La misma cotización otra vez es la misma operación: no cambia dos veces.
	_, again := e.call(t, http.MethodPost, "/v1/fx/conversions", token, map[string]any{"quote_id": q["quote_id"]})
	if again["duplicate"] != true || e.balance(t, token, usd) != 150_000_000 {
		t.Errorf("repetir la cotización: %v", again)
	}

	// Y de vuelta: el banco vende al 0,5 % más (3,3969). S/ 100 dan 29,43 USD.
	_, back := e.call(t, http.MethodPost, "/v1/fx/quotes", token, map[string]any{
		"from_account_id": pen, "to_account_id": usd, "amount_micros": 100_000_000})
	if back["applied_rate"] != float64(3_396_900) || back["to"].(map[string]any)["amount_micros"] != float64(29_430_000) {
		t.Errorf("soles a dólares: %v", back)
	}
}

func TestUnaCotizacionVencidaOAjenaNoSeUsa(t *testing.T) {
	e := newPasskeyEnv(t)
	_, token, usd, _ := e.linkedWithFunds(t)
	pen := e.openAccount(t, token, "PEN")
	_, q := e.call(t, http.MethodPost, "/v1/fx/quotes", token, map[string]any{
		"from_account_id": usd, "to_account_id": pen, "amount_micros": 10_000_000})

	_, otherToken, otherUSD, _ := e.linkedWithFunds(t)
	if status, _ := e.call(t, http.MethodPost, "/v1/fx/conversions", otherToken, map[string]any{"quote_id": q["quote_id"]}); status != http.StatusNotFound {
		t.Errorf("la cotización de otro: %d", status)
	}
	if status, _ := e.call(t, http.MethodPost, "/v1/fx/quotes", token, map[string]any{
		"from_account_id": usd, "to_account_id": otherUSD, "amount_micros": 10_000_000}); status != http.StatusNotFound {
		t.Errorf("cambiar hacia la cuenta de otro: %d", status)
	}

	e.fx.SetClock(func() time.Time { return time.Now().Add(time.Minute) })
	defer e.fx.SetClock(time.Now)
	if status, out := e.call(t, http.MethodPost, "/v1/fx/conversions", token, map[string]any{"quote_id": q["quote_id"]}); status != http.StatusConflict || out["code"] != "quote_expired" {
		t.Errorf("cotización vencida: %d %v", status, out)
	}
}
