package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func corsServer(origins string) http.Handler {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return withCORS(ok, parseOrigins(origins))
}

func TestElPortalAutorizadoPuedeLlamar(t *testing.T) {
	req := httptest.NewRequest("GET", "/v1/home", nil)
	req.Header.Set("Origin", "http://localhost:5050")
	rec := httptest.NewRecorder()
	corsServer("http://localhost:5050").ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:5050" {
		t.Fatalf("Allow-Origin = %q", got)
	}
}

// Una página cualquiera no recibe permiso: el navegador del cliente bloquea la
// respuesta antes de que esa página pueda leerla.
func TestUnOrigenNoListadoNoRecibePermiso(t *testing.T) {
	req := httptest.NewRequest("GET", "/v1/home", nil)
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	corsServer("http://localhost:5050").ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("se dio permiso a un origen no listado: %q", got)
	}
}

// Sin lista no hay CORS, y un asterisco escrito por error no lo abre a todo el
// mundo: se ignora.
func TestNiVacioNiAsteriscoAbrenElBanco(t *testing.T) {
	for _, config := range []string{"", "*", " * , "} {
		req := httptest.NewRequest("GET", "/v1/home", nil)
		req.Header.Set("Origin", "https://cualquiera.example")
		rec := httptest.NewRecorder()
		corsServer(config).ServeHTTP(rec, req)

		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Fatalf("con %q se dio permiso: %q", config, got)
		}
	}
}

// Sin Idempotency-Key en la lista, el navegador no dejaría pasar una
// transferencia: el preflight la rechazaría antes de llegar al servidor.
func TestElPreflightAdmiteLaClaveDeIdempotencia(t *testing.T) {
	req := httptest.NewRequest("OPTIONS", "/v1/transfers", nil)
	req.Header.Set("Origin", "http://localhost:5050")
	req.Header.Set("Access-Control-Request-Method", "POST")
	rec := httptest.NewRecorder()
	corsServer("http://localhost:5050").ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight = %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); got != corsAllowedHeaders {
		t.Fatalf("Allow-Headers = %q", got)
	}
}

func TestElPreflightDeUnOrigenAjenoSeRechaza(t *testing.T) {
	req := httptest.NewRequest("OPTIONS", "/v1/transfers", nil)
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Access-Control-Request-Method", "POST")
	rec := httptest.NewRecorder()
	corsServer("http://localhost:5050").ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("preflight ajeno = %d, se esperaba 403", rec.Code)
	}
}

// Una barra final en la configuración es el error más común y no debe romper
// el portal: el navegador manda el origen sin ella.
func TestLaBarraFinalNoImporta(t *testing.T) {
	req := httptest.NewRequest("GET", "/v1/home", nil)
	req.Header.Set("Origin", "http://localhost:5050")
	rec := httptest.NewRecorder()
	corsServer("http://localhost:5050/").ServeHTTP(rec, req)

	if rec.Header().Get("Access-Control-Allow-Origin") == "" {
		t.Fatal("la barra final impidió reconocer el origen")
	}
}
