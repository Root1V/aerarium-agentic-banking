package bff_test

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
)

// Los bolsillos por HTTP: apartar, devolver, cerrar, y lo que no se puede.

func (e *passkeyEnv) openPocket(t *testing.T, token, account, name, purpose string) map[string]any {
	t.Helper()
	status, out := e.call(t, http.MethodPost, "/v1/accounts/"+account+"/pockets", token,
		map[string]any{"name": name, "purpose": purpose})
	if status != http.StatusCreated {
		t.Fatalf("abrir bolsillo: %d %v", status, out)
	}
	return out
}

func (e *passkeyEnv) pocketMove(t *testing.T, token, pocket, way string, amount int64) (int, map[string]any) {
	t.Helper()
	return e.callWith(t, http.MethodPost, "/v1/pockets/"+pocket+"/"+way, token,
		map[string]string{"Idempotency-Key": uuid.NewString()}, map[string]any{"amount_micros": amount})
}

func TestApartarEnUnBolsilloYDevolverlo(t *testing.T) {
	e := newPasskeyEnv(t)
	_, token, account, _ := e.linkedWithFunds(t) // 250 de saldo
	pocket := e.openPocket(t, token, account, "Vacaciones", "general")["id"].(string)

	if status, out := e.pocketMove(t, token, pocket, "deposit", 100_000_000); status != http.StatusCreated {
		t.Fatalf("apartar: %d %v", status, out)
	}
	if got := e.balance(t, token, account); got != 150_000_000 {
		t.Errorf("disponible tras apartar: %v", got)
	}
	_, list := e.call(t, http.MethodGet, "/v1/accounts/"+account+"/pockets", token, nil)
	pockets := list["pockets"].([]any)
	if len(pockets) != 1 || pockets[0].(map[string]any)["balance"].(map[string]any)["amount_micros"] != float64(100_000_000) {
		t.Errorf("bolsillos: %v", list)
	}

	if status, out := e.pocketMove(t, token, pocket, "withdraw", 30_000_000); status != http.StatusCreated {
		t.Fatalf("devolver: %d %v", status, out)
	}
	if status, out := e.pocketMove(t, token, pocket, "withdraw", 500_000_000); status != http.StatusUnprocessableEntity || out["code"] != "insufficient_funds" {
		t.Errorf("devolver más de lo apartado: %d %v", status, out)
	}

	// Cerrar devuelve lo que quede: el dinero no desaparece.
	if status, _ := e.call(t, http.MethodDelete, "/v1/pockets/"+pocket, token, nil); status != http.StatusNoContent {
		t.Fatalf("cerrar: %d", status)
	}
	if got := e.balance(t, token, account); got != 250_000_000 {
		t.Errorf("disponible tras cerrar: %v", got)
	}
	_, list = e.call(t, http.MethodGet, "/v1/accounts/"+account+"/pockets", token, nil)
	if len(list["pockets"].([]any)) != 0 {
		t.Errorf("el bolsillo cerrado sigue en la lista: %v", list)
	}
}

func TestUnSoloBolsilloParaLaTarjeta(t *testing.T) {
	e := newPasskeyEnv(t)
	_, token, account, _ := e.linkedWithFunds(t)
	card := e.openPocket(t, token, account, "Para la tarjeta", "card")["id"].(string)
	status, out := e.call(t, http.MethodPost, "/v1/accounts/"+account+"/pockets", token,
		map[string]any{"name": "Otra para la tarjeta", "purpose": "card"})
	if status != http.StatusConflict {
		t.Errorf("segundo bolsillo para la tarjeta: %d %v", status, out)
	}
	// Cerrado el primero, se puede abrir otro.
	e.call(t, http.MethodDelete, "/v1/pockets/"+card, token, nil)
	e.openPocket(t, token, account, "Para la tarjeta", "card")
}

// Lo apartado no se manda a otros por descuido, ni otro toca tus bolsillos.
func TestUnBolsilloNoSeTransfiereNiLoTocaOtro(t *testing.T) {
	e := newPasskeyEnv(t)
	_, token, account, _ := e.linkedWithFunds(t)
	_, otherToken, otherAccount, _ := e.linkedWithFunds(t)
	pocket := e.openPocket(t, token, account, "Ahorro", "general")["id"].(string)
	e.pocketMove(t, token, pocket, "deposit", 50_000_000)

	status, out := e.send(t, token, uuid.NewString(), pocket, otherAccount, 10_000_000, nil)
	if status != http.StatusUnprocessableEntity || out["code"] != "pocket_not_transferable" {
		t.Errorf("transferir desde un bolsillo: %d %v", status, out)
	}
	if status, _ := e.pocketMove(t, otherToken, pocket, "withdraw", 10_000_000); status != http.StatusNotFound {
		t.Errorf("otro sacando de mi bolsillo: %d", status)
	}
	if status, _ := e.call(t, http.MethodDelete, "/v1/pockets/"+pocket, otherToken, nil); status != http.StatusNotFound {
		t.Errorf("otro cerrando mi bolsillo: %d", status)
	}
	// Y los bolsillos no aparecen como cuentas para entrar.
	_, accounts := e.call(t, http.MethodGet, "/v1/accounts", token, nil)
	for _, a := range accounts["accounts"].([]any) {
		if a.(map[string]any)["id"] == pocket {
			t.Errorf("el bolsillo aparece entre las cuentas: %v", accounts)
		}
	}
}

// La previsión da por reservado para la tarjeta lo que está en su bolsillo.
func TestLaPrevisionCuentaElBolsilloDeLaTarjeta(t *testing.T) {
	e := newPasskeyEnv(t)
	_, token, account, _ := e.linkedWithFunds(t)
	e.call(t, http.MethodPost, "/v1/credit/applications", token, map[string]any{"account_id": account})
	e.creditMove(t, "/v1/credit/card/draws", token, account, 200_000_000) // 450 en la cuenta
	e.call(t, http.MethodPost, "/v1/sandbox/credit/close-cycle", token, nil)
	pocket := e.openPocket(t, token, account, "Para la tarjeta", "card")["id"].(string)
	e.pocketMove(t, token, pocket, "deposit", 150_000_000) // 300 disponibles, 150 apartados

	_, f := e.call(t, http.MethodGet, "/v1/accounts/"+account+"/forecast", token, nil)
	card := f["card"].(map[string]any)
	if card["reserved"].(map[string]any)["amount_micros"] != float64(150_000_000) {
		t.Fatalf("apartado para la tarjeta: %v", card)
	}
	// 300 − (200 − 150) = 250 el día que vence.
	if f["after_card"].(map[string]any)["amount"].(map[string]any)["amount_micros"] != float64(250_000_000) {
		t.Errorf("al pagar la tarjeta: %v", f["after_card"])
	}
}
