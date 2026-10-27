package bff

import (
	"context"
	"net/http"

	"github.com/aibank/aibank/services/sessions"
)

// WithSessions habilita cerrar sesión en el banco.
func (s *Server) WithSessions(store *sessions.Store) *Server {
	s.sessions = store
	return s
}

// SessionAuthenticator resuelve los tokens emitidos tras entrar, sea con passkey
// o con el teléfono vinculado.
type SessionAuthenticator struct{ store *sessions.Store }

func NewSessionAuthenticator(store *sessions.Store) *SessionAuthenticator {
	return &SessionAuthenticator{store: store}
}

func (a *SessionAuthenticator) Authenticate(ctx context.Context, token string) (*Principal, error) {
	p, err := a.store.Resolve(ctx, token)
	if err != nil {
		return nil, ErrUnauthenticated
	}
	// Lo que firmó —el teléfono o la passkey— identifica el dispositivo: es lo
	// que permite exigir que una operación sensible venga del mismo.
	return &Principal{CustomerID: p.CustomerID, DeviceID: p.DeviceRef}, nil
}

// ChainAuthenticator prueba varios autenticadores en orden.
//
// Existe para el sandbox, donde conviven las sesiones reales y el sustituto de
// desarrollo. En producción la cadena tiene un solo eslabón.
type ChainAuthenticator []Authenticator

func (c ChainAuthenticator) Authenticate(ctx context.Context, token string) (*Principal, error) {
	for _, a := range c {
		if p, err := a.Authenticate(ctx, token); err == nil {
			return p, nil
		}
	}
	return nil, ErrUnauthenticated
}

// handleLogout cierra la sesión también en el banco, no solo en el teléfono: un
// token olvidado en memoria tiene que dejar de valer del otro lado.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request, _ *Principal) {
	if err := s.sessions.Revoke(r.Context(), bearerToken(r)); err != nil {
		s.logger.ErrorContext(r.Context(), "cerrar sesión", "error", err)
		writeError(w, http.StatusServiceUnavailable, "unavailable", "inténtalo de nuevo")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
