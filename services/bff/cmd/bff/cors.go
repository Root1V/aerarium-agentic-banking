package main

import (
	"net/http"
	"strings"
)

// CORS para el portal web.
//
// La app móvil no lo necesita: no es un navegador. El portal web sí, porque se
// sirve desde un origen distinto al del BFF y el navegador bloquea sus llamadas
// si el BFF no declara que lo admite.
//
// Se admite una lista EXACTA de orígenes, nunca `*`. Con `*`, cualquier página
// de internet podría hacer que el navegador de un cliente llame al banco; el
// token va en una cabecera y no en una cookie, así que no se adjuntaría solo,
// pero una lista cerrada no depende de ese detalle para ser segura.
//
// Sin orígenes configurados no hay CORS en absoluto: es el comportamiento de
// siempre, y el que corresponde a un BFF que solo sirve a la app móvil.

// Cabeceras que el portal manda. Idempotency-Key es imprescindible: sin ella el
// navegador no dejaría pasar una transferencia.
const corsAllowedHeaders = "Authorization, Content-Type, Idempotency-Key, X-Dev-Key"

func parseOrigins(raw string) map[string]bool {
	origins := make(map[string]bool)
	for _, origin := range strings.Split(raw, ",") {
		origin = strings.TrimRight(strings.TrimSpace(origin), "/")
		if origin != "" && origin != "*" {
			origins[origin] = true
		}
	}
	return origins
}

func withCORS(next http.Handler, allowed map[string]bool) http.Handler {
	if len(allowed) == 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")

		// La respuesta depende del origen: sin esto, una caché intermedia
		// podría servirle a un origen la respuesta preparada para otro.
		w.Header().Add("Vary", "Origin")

		if origin != "" && allowed[origin] {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Headers", corsAllowedHeaders)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST")
			w.Header().Set("Access-Control-Max-Age", "600")
		}

		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			if !allowed[origin] {
				// Un 403 explícito en vez de un 204 sin cabeceras: el resultado
				// para el navegador es el mismo, pero en los logs se ve quién
				// intentó entrar desde dónde.
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}
