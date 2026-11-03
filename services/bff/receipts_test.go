package bff_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// Las constancias verificables: quien recibe un pago comprueba con el banco que
// es real, sin ser cliente y sin depender de una captura.
func TestUnaConstanciaLaVerificaCualquieraConSuCodigo(t *testing.T) {
	e := newPasskeyEnv(t)
	h, token, account, payee := e.linkedWithFunds(t)
	key := uuid.NewString()
	_, need := e.send(t, token, key, account, payee, 50_000_000, nil)
	_, sent := e.send(t, token, key, account, payee, 50_000_000, map[string]any{
		"challenge_id": need["challenge_id"], "method": "biometric", "signature": signB64(t, h.bio, need["payload"]),
	})
	tx := sent["transaction_id"].(string)

	status, receipt := e.call(t, http.MethodPost, "/v1/accounts/"+account+"/movements/"+tx+"/receipt", token, nil)
	if status != http.StatusCreated {
		t.Fatalf("emitir: %d %v", status, receipt)
	}
	code := receipt["code"].(string)
	if len(code) != 11 || code[5] != '-' || !strings.HasSuffix(receipt["url"].(string), "/c/"+code) {
		t.Errorf("constancia: %v", receipt)
	}
	// Pedirla otra vez da la misma.
	if _, again := e.call(t, http.MethodPost, "/v1/accounts/"+account+"/movements/"+tx+"/receipt", token, nil); again["code"] != code {
		t.Errorf("segunda emisión: %v", again)
	}

	// Sin sesión, escrita a mano: minúsculas y sin guion.
	status, verified := e.call(t, http.MethodGet, "/v1/receipts/"+strings.ToLower(strings.ReplaceAll(code, "-", "")), "", nil)
	if status != http.StatusOK || verified["status"] != "completed" ||
		verified["amount"].(map[string]any)["amount_micros"] != float64(50_000_000) {
		t.Fatalf("verificar: %d %v", status, verified)
	}
	// Los nombres van acortados: bastan para reconocer, no para más.
	if from := verified["from"].(string); !strings.HasSuffix(from, ".") || verified["to"] == "" {
		t.Errorf("nombres: %v", verified)
	}

	if status, out := e.call(t, http.MethodGet, "/v1/receipts/ABCDE-FGHJK", "", nil); status != http.StatusNotFound || out["code"] != "receipt_not_found" {
		t.Errorf("código inventado: %d %v", status, out)
	}

	// La página, para quien no tiene la app.
	resp, err := http.Get(e.srv.URL + "/c/" + code)
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(page), "Este pago es real") ||
		!strings.Contains(string(page), "50,00 USD") {
		t.Errorf("página: %d %s", resp.StatusCode, page)
	}
}

func TestSoloLlevaConstanciaLoQueSePagaYSoloElDueno(t *testing.T) {
	e := newPasskeyEnv(t)
	_, token, account, _ := e.linkedWithFunds(t)
	pocket := e.openPocket(t, token, account, "Ahorro", "general")["id"].(string)
	e.pocketMove(t, token, pocket, "deposit", 10_000_000)
	_, page := e.call(t, http.MethodGet, "/v1/accounts/"+account+"/movements", token, nil)
	var pocketTx string
	for _, m := range page["movements"].([]any) {
		if mv := m.(map[string]any); mv["kind"] == "pocket_transfer" {
			pocketTx = mv["id"].(string)
		}
	}
	if status, _ := e.call(t, http.MethodPost, "/v1/accounts/"+account+"/movements/"+pocketTx+"/receipt", token, nil); status != http.StatusUnprocessableEntity {
		t.Errorf("constancia de un bolsillo: %d", status)
	}
	_, otherToken, _, _ := e.linkedWithFunds(t)
	if status, _ := e.call(t, http.MethodPost, "/v1/accounts/"+account+"/movements/"+pocketTx+"/receipt", otherToken, nil); status != http.StatusNotFound {
		t.Errorf("constancia de una cuenta ajena: %d", status)
	}
}
