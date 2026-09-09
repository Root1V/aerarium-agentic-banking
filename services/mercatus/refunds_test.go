package mercatus

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
)

// Reembolso parcial: devolver menos que el total de un cobro.
//
// La operación de soporte casi siempre termina necesitándolo, y el modelo del
// core lo soportaba desde el principio. Lo que se prueba aquí es que exponerlo
// no rompió el reembolso total, que es el caso normal.

// capturedAuthorization deja una autorización cobrada y lista para devolver.
func (f *fixture) capturedAuthorization(t *testing.T, amount int64) string {
	t.Helper()
	status, auth, _ := f.authorize(t, amount)
	if status != http.StatusCreated {
		t.Fatalf("autorizar: %d, %v", status, auth)
	}
	id := auth["authorization_id"].(string)
	if status, out, _ := f.do(t, http.MethodPost,
		"/v1/authorizations/"+id+"/capture", f.token, nil, nil); status != http.StatusOK {
		t.Fatalf("capturar: %d, %v", status, out)
	}
	return id
}

func TestElReembolsoTotalSigueSiendoUnaLlamadaSinCuerpo(t *testing.T) {
	f := setup(t)
	id := f.capturedAuthorization(t, 10_000000)

	status, out, _ := f.do(t, http.MethodPost, "/v1/authorizations/"+id+"/refund", f.token, nil, nil)

	// Un cliente escrito antes de que existiera el parcial no cambia una línea.
	if status != http.StatusOK || out["status"] != "refunded" {
		t.Fatalf("status = %d, %v", status, out)
	}
	if out["refunded"] != float64(10_000000) || out["refundable"] != float64(0) {
		t.Errorf("refunded = %v, refundable = %v", out["refunded"], out["refundable"])
	}
}

func TestSePuedeDevolverSoloUnaParte(t *testing.T) {
	f := setup(t)
	id := f.capturedAuthorization(t, 10_000000)

	status, out, _ := f.do(t, http.MethodPost, "/v1/authorizations/"+id+"/refund",
		f.token, map[string]any{"amount": 3_000000}, nil)

	if status != http.StatusOK {
		t.Fatalf("status = %d, %v", status, out)
	}
	// El estado lo dice: quedó a medio devolver, no devuelto.
	if out["status"] != "partially_refunded" {
		t.Errorf("status = %v", out["status"])
	}
	if out["refunded"] != float64(3_000000) {
		t.Errorf("refunded = %v", out["refunded"])
	}
	// Lo que queda por devolver viene en la respuesta para que soporte no tenga
	// que llevar la cuenta por su lado.
	if out["refundable"] != float64(7_000000) {
		t.Errorf("refundable = %v", out["refundable"])
	}
}

func TestVariosParcialesSumanHastaCompletarElTotal(t *testing.T) {
	f := setup(t)
	id := f.capturedAuthorization(t, 10_000000)

	f.do(t, http.MethodPost, "/v1/authorizations/"+id+"/refund",
		f.token, map[string]any{"amount": 4_000000}, nil)
	status, out, _ := f.do(t, http.MethodPost, "/v1/authorizations/"+id+"/refund",
		f.token, map[string]any{"amount": 6_000000}, nil)

	if status != http.StatusOK || out["status"] != "refunded" {
		t.Fatalf("status = %d, %v", status, out)
	}
	if out["refunded_total"] != float64(10_000000) || out["refundable"] != float64(0) {
		t.Errorf("refunded_total = %v, refundable = %v", out["refunded_total"], out["refundable"])
	}

	// Y el dinero volvió entero: ni de más ni de menos.
	balance, err := f.core.GetBalance(f.ctx, mustDecode(t, accountPrefix, f.payee))
	if err != nil {
		t.Fatalf("saldo: %v", err)
	}
	if balance.AmountMicros != 0 {
		t.Errorf("el receptor quedó con %d micras", balance.AmountMicros)
	}
}

func TestNoSeDevuelveMasDeLoQueQueda(t *testing.T) {
	f := setup(t)
	id := f.capturedAuthorization(t, 10_000000)

	f.do(t, http.MethodPost, "/v1/authorizations/"+id+"/refund",
		f.token, map[string]any{"amount": 8_000000}, nil)
	status, out, _ := f.do(t, http.MethodPost, "/v1/authorizations/"+id+"/refund",
		f.token, map[string]any{"amount": 5_000000}, nil)

	// 422 y no 400: la petición está bien formada, lo que no cuadra es el monto
	// contra el estado del cobro.
	if status != http.StatusUnprocessableEntity || out["error"] != CodeRefundExceedsCapture {
		t.Errorf("status = %d, error = %v", status, out["error"])
	}

	// Solo volvieron los 8, no 13.
	balance, _ := f.core.GetBalance(f.ctx, mustDecode(t, accountPrefix, f.payee))
	if balance.AmountMicros != 2_000000 {
		t.Errorf("saldo del receptor = %d, se esperaban 2_000000", balance.AmountMicros)
	}
}

func TestUnMontoNegativoSeRechaza(t *testing.T) {
	f := setup(t)
	id := f.capturedAuthorization(t, 10_000000)

	status, out, _ := f.do(t, http.MethodPost, "/v1/authorizations/"+id+"/refund",
		f.token, map[string]any{"amount": -1}, nil)

	if status != http.StatusBadRequest || out["error"] != CodeMalformedRequest {
		t.Errorf("status = %d, error = %v", status, out["error"])
	}
}

func TestReembolsarLoNoCobradoSigueSiendoUnConflicto(t *testing.T) {
	f := setup(t)
	status, auth, _ := f.authorize(t, 5_000000)
	if status != http.StatusCreated {
		t.Fatalf("autorizar: %d", status)
	}
	id := auth["authorization_id"].(string)

	status, out, _ := f.do(t, http.MethodPost, "/v1/authorizations/"+id+"/refund",
		f.token, map[string]any{"amount": 1_000000}, nil)

	// Devolver una retención le daría al pagador un dinero que nunca perdió.
	if status != http.StatusConflict || out["error"] != CodeNothingToRefund {
		t.Errorf("status = %d, error = %v", status, out["error"])
	}
}

func TestUnaAutorizacionAjenaNoSePuedeReembolsar(t *testing.T) {
	f := setup(t)
	id := f.capturedAuthorization(t, 5_000000)

	otro := "otro_" + uuid.NewString()
	secret, err := f.store.Register(f.ctx, otro, "Otra plataforma",
		[]string{"payments:read", "payments:write"})
	if err != nil {
		t.Fatalf("registrar: %v", err)
	}

	status, out, _ := f.do(t, http.MethodPost, "/v1/authorizations/"+id+"/refund",
		f.fetchToken(t, f.srv, otro, secret, ""), map[string]any{"amount": 1_000000}, nil)

	if status != http.StatusNotFound || out["error"] != CodeAuthorizationMissing {
		t.Errorf("status = %d, error = %v", status, out["error"])
	}
}
