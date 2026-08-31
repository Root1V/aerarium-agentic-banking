package bff

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

// ErrUnauthenticated: el token falta, expiró o no es válido.
var ErrUnauthenticated = errors.New("no autenticado")

// Principal es la identidad del cliente detrás de una petición.
type Principal struct {
	CustomerID string
	// DeviceID permite exigir vinculación de dispositivo en operaciones sensibles.
	DeviceID string
}

// Authenticator resuelve un token a la identidad del cliente.
//
// Deliberadamente es un PUERTO y no una implementación: la autenticación de banca
// se resuelve con passkeys FIDO2 vinculadas al dispositivo y verificación
// biométrica, no con un usuario y contraseña propios. Dejar aquí una interfaz —
// en vez de un login provisional— evita que un sustituto de desarrollo termine
// cableado a producción.
type Authenticator interface {
	Authenticate(ctx context.Context, token string) (*Principal, error)
}

// bearerToken extrae el token del encabezado Authorization.
func bearerToken(r *http.Request) string {
	header := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(header[len(prefix):])
}

type principalKey struct{}

// withPrincipal guarda la identidad autenticada en el contexto de la petición.
func withPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// principalFrom recupera la identidad. Solo tiene valor tras pasar por requireAuth.
func principalFrom(ctx context.Context) (*Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(*Principal)
	return p, ok
}
