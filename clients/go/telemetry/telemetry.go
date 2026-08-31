// Package telemetry centraliza el trazado distribuido de los servicios Go.
//
// Existe para que la propagación y las reglas de qué se puede registrar se
// decidan UNA vez y no en cada servicio. La regla dura: en una traza nunca entran
// datos personales ni secretos — las trazas salen a herramientas de terceros y se
// conservan mucho tiempo.
package telemetry

import (
	"context"
	"net/http"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// Init configura la propagación W3C, que es lo que permite que el contexto cruce
// entre Go y Rust.
//
// No configura un exportador: cada servicio decide a dónde manda sus trazas. Sin
// exportador, las operaciones siguen funcionando y la propagación también — la
// telemetría nunca debe ser un punto de fallo del dinero.
func Init() {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
}

// Tracer devuelve el trazador de un servicio.
func Tracer(service string) trace.Tracer {
	return otel.Tracer(service)
}

// FromRequest recupera el contexto de traza que llegó en una petición HTTP.
func FromRequest(r *http.Request) context.Context {
	return otel.GetTextMapPropagator().Extract(
		r.Context(), propagation.HeaderCarrier(r.Header))
}

// TraceID devuelve el identificador de traza, o vacío si no hay traza activa.
//
// Se registra junto a los eventos de log para poder saltar del log a la traza: es
// el puente que convierte "algo falló" en "esto fue lo que pasó".
func TraceID(ctx context.Context) string {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return ""
	}
	return sc.TraceID().String()
}

// forbidden son fragmentos que descalifican un atributo.
//
// La comparación es por subcadena y deliberadamente amplia: `customer_email` o
// `card_number_hash` también quedan fuera. Ante la duda, no se registra.
var forbidden = []string{
	"pan", "card_number", "cvv", "document_number", "full_name",
	"email", "phone", "password", "token", "authorization", "secret", "api_key",
}

// IsSafeAttribute indica si un atributo puede registrarse en una traza.
func IsSafeAttribute(key string) bool {
	lowered := strings.ToLower(key)
	for _, f := range forbidden {
		if strings.Contains(lowered, f) {
			return false
		}
	}
	return true
}

// SafeAttributes descarta los atributos que no pueden registrarse.
//
// Se usa donde los atributos provienen de datos externos y no de literales del
// código, que es justo donde se cuela una fuga.
func SafeAttributes(attrs ...attribute.KeyValue) []attribute.KeyValue {
	safe := make([]attribute.KeyValue, 0, len(attrs))
	for _, attr := range attrs {
		if IsSafeAttribute(string(attr.Key)) {
			safe = append(safe, attr)
		}
	}
	return safe
}

// Middleware envuelve un handler HTTP recuperando el contexto entrante y
// abriendo un span por petición.
func Middleware(service string, next http.Handler) http.Handler {
	tracer := Tracer(service)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, span := tracer.Start(FromRequest(r), r.Method+" "+r.URL.Path)
		defer span.End()

		// Solo la ruta y el método: la query puede llevar identificadores de
		// cuenta y no aporta nada que compense el riesgo.
		span.SetAttributes(
			attribute.String("http.method", r.Method),
			attribute.String("http.route", r.URL.Path),
		)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
