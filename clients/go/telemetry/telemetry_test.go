package telemetry_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aibank/aibank/clients/go/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

const sampleTraceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
const sampleTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"

func TestElContextoSePropagaDesdeUnaPeticionHTTP(t *testing.T) {
	telemetry.Init()

	req := httptest.NewRequest(http.MethodGet, "/v1/home", nil)
	req.Header.Set("traceparent", sampleTraceparent)

	ctx := telemetry.FromRequest(req)

	if got := telemetry.TraceID(ctx); got != sampleTraceID {
		t.Errorf("trace id = %q, se esperaba %q: el servicio debe adoptar la traza del llamador", got, sampleTraceID)
	}
}

func TestUnaPeticionSinTrazaNoFalla(t *testing.T) {
	telemetry.Init()

	// La telemetría nunca debe ser un punto de fallo de una operación de dinero.
	ctx := telemetry.FromRequest(httptest.NewRequest(http.MethodGet, "/v1/home", nil))
	if got := telemetry.TraceID(ctx); got != "" {
		t.Errorf("sin traza entrante no debe inventarse una, se obtuvo %q", got)
	}
}

func TestElMiddlewarePropagaLaTrazaAlHandler(t *testing.T) {
	telemetry.Init()

	var seen string
	handler := telemetry.Middleware("test", http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = telemetry.TraceID(r.Context())
	}))

	req := httptest.NewRequest(http.MethodGet, "/v1/home", nil)
	req.Header.Set("traceparent", sampleTraceparent)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if seen != sampleTraceID {
		t.Errorf("el handler vio %q, se esperaba %q", seen, sampleTraceID)
	}
}

// Las trazas salen a herramientas de terceros y se conservan mucho tiempo.
func TestLosDatosPersonalesYSecretosNoSeRegistran(t *testing.T) {
	for _, key := range []string{
		"pan", "card_number", "cvv", "document_number", "customer_email",
		"phone", "password", "auth_token", "api_key", "client_secret",
		"CARD_NUMBER_HASH", "userEmail",
	} {
		if telemetry.IsSafeAttribute(key) {
			t.Errorf("%q no debería poder registrarse en una traza", key)
		}
	}
}

func TestLosIdentificadoresDeNegocioSiSeRegistran(t *testing.T) {
	for _, key := range []string{
		"transaction_id", "account_id", "idempotency_key",
		"amount_micros", "currency", "kind", "network_transaction_id",
	} {
		if !telemetry.IsSafeAttribute(key) {
			t.Errorf("%q debería poder registrarse: sin identificadores no hay traza útil", key)
		}
	}
}

func TestElFiltroDescartaSoloLoProhibido(t *testing.T) {
	filtered := telemetry.SafeAttributes(
		attribute.String("transaction_id", "tx-1"),
		attribute.String("document_number", "12345678"),
		attribute.Int64("amount_micros", 12000),
		attribute.String("password", "hunter2"),
	)

	if len(filtered) != 2 {
		t.Fatalf("atributos = %d, se esperaban 2: %v", len(filtered), filtered)
	}
	for _, attr := range filtered {
		if !telemetry.IsSafeAttribute(string(attr.Key)) {
			t.Errorf("se coló un atributo prohibido: %s", attr.Key)
		}
	}
}
