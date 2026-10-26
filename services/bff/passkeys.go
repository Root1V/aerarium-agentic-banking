package bff

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/aibank/aibank/services/passkeys"
	"github.com/aibank/aibank/services/ratelimit"
)

// Passkeys en el canal del titular: entrar, registrar el primer dispositivo tras
// el alta, añadir otro, y cerrar sesión.
//
// Las ceremonias son públicas —quien entra todavía no tiene sesión— y llevan
// límite por origen. Ante una passkey desconocida, una firma que no verifica o
// una passkey clonada, la respuesta es la MISMA: distinguirlas le diría a quien
// prueba qué credenciales existen. La diferencia queda en el log, que es donde
// la necesita seguridad.

const (
	defaultPasskeyRate  = 2
	defaultPasskeyBurst = 10
)

type passkeyConfig struct {
	svc     *passkeys.Service
	limiter *ratelimit.Limiter
}

// WithPasskeys habilita el login con passkeys y sus ceremonias.
func (s *Server) WithPasskeys(svc *passkeys.Service) *Server {
	s.passkeys = &passkeyConfig{
		svc:     svc,
		limiter: ratelimit.NewLimiter(defaultPasskeyRate, defaultPasskeyBurst),
	}
	return s
}

// PasskeyAuthenticator resuelve los tokens emitidos tras una ceremonia superada.
type PasskeyAuthenticator struct{ svc *passkeys.Service }

func NewPasskeyAuthenticator(svc *passkeys.Service) *PasskeyAuthenticator {
	return &PasskeyAuthenticator{svc: svc}
}

func (a *PasskeyAuthenticator) Authenticate(ctx context.Context, token string) (*Principal, error) {
	customer, device, err := a.svc.ResolveSession(ctx, token)
	if err != nil {
		return nil, ErrUnauthenticated
	}
	// La passkey identifica el dispositivo: es lo que permitirá exigir que una
	// operación sensible venga del mismo desde el que se entró.
	return &Principal{CustomerID: customer, DeviceID: device}, nil
}

// ChainAuthenticator prueba varios autenticadores en orden.
//
// Existe para el sandbox, donde conviven las passkeys y el sustituto de
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

type ceremonyResponse struct {
	CeremonyID string `json:"ceremony_id"`
	// Options es lo que la app le pasa tal cual al sistema operativo.
	Options any `json:"options"`
}

type finishRequest struct {
	CeremonyID string `json:"ceremony_id"`
	// Credential es la respuesta del autenticador, en la serialización JSON de
	// WebAuthn. Se reenvía sin tocar a la verificación.
	Credential json.RawMessage `json:"credential"`
}

type sessionResponse struct {
	Token      string    `json:"token"`
	CustomerID string    `json:"customer_id"`
	ExpiresAt  time.Time `json:"expires_at"`
}

func (s *Server) passkeyRoutes(mux *http.ServeMux) {
	limited := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if ok, wait := s.passkeys.limiter.Allow(clientIP(r)); !ok {
				w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
				writeError(w, http.StatusTooManyRequests, "rate_limited", "demasiados intentos; espera un momento")
				return
			}
			h(w, r)
		}
	}

	mux.HandleFunc("POST /v1/passkeys/enrollment", limited(s.handleBeginEnrollment))
	mux.HandleFunc("POST /v1/passkeys/registration", limited(s.handleFinishRegistration))
	mux.HandleFunc("POST /v1/passkeys/login/options", limited(s.handleBeginLogin))
	mux.HandleFunc("POST /v1/passkeys/login", limited(s.handleFinishLogin))

	mux.Handle("POST /v1/passkeys/devices", s.requireAuth(s.handleBeginAddDevice))
	mux.Handle("POST /v1/session/logout", s.requireAuth(s.handleLogout))
}

func (s *Server) handleBeginEnrollment(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Reference string `json:"reference"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	id, options, err := s.passkeys.svc.BeginEnrollment(r.Context(), body.Reference)
	if errors.Is(err, passkeys.ErrNotEnrollable) {
		// Mismo mensaje para "no existe", "no está aprobada", "ya tiene passkey"
		// y "venció": quien prueba referencias no aprende nada.
		writeError(w, http.StatusForbidden, "not_enrollable",
			"esa alta no habilita registrar una passkey")
		return
	}
	if err != nil {
		s.logger.ErrorContext(r.Context(), "passkeys: empezar registro", "error", err)
		writeError(w, http.StatusServiceUnavailable, "unavailable", "inténtalo de nuevo")
		return
	}
	writeJSON(w, http.StatusOK, ceremonyResponse{CeremonyID: id, Options: options})
}

func (s *Server) handleBeginAddDevice(w http.ResponseWriter, r *http.Request, p *Principal) {
	id, options, err := s.passkeys.svc.BeginAddDevice(r.Context(), p.CustomerID)
	if errors.Is(err, passkeys.ErrNotEnrollable) {
		writeError(w, http.StatusForbidden, "not_enrollable",
			"añadir un dispositivo requiere haber entrado con una passkey")
		return
	}
	if err != nil {
		s.logger.ErrorContext(r.Context(), "passkeys: añadir dispositivo", "error", err)
		writeError(w, http.StatusServiceUnavailable, "unavailable", "inténtalo de nuevo")
		return
	}
	writeJSON(w, http.StatusOK, ceremonyResponse{CeremonyID: id, Options: options})
}

func (s *Server) handleFinishRegistration(w http.ResponseWriter, r *http.Request) {
	var body finishRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	session, err := s.passkeys.svc.FinishRegistration(r.Context(), body.CeremonyID, body.Credential)
	if err != nil {
		s.ceremonyError(w, r, "registro", err)
		return
	}
	writeJSON(w, http.StatusCreated, toSessionResponse(session))
}

func (s *Server) handleBeginLogin(w http.ResponseWriter, r *http.Request) {
	id, options, err := s.passkeys.svc.BeginLogin(r.Context())
	if err != nil {
		s.logger.ErrorContext(r.Context(), "passkeys: empezar login", "error", err)
		writeError(w, http.StatusServiceUnavailable, "unavailable", "inténtalo de nuevo")
		return
	}
	writeJSON(w, http.StatusOK, ceremonyResponse{CeremonyID: id, Options: options})
}

func (s *Server) handleFinishLogin(w http.ResponseWriter, r *http.Request) {
	var body finishRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	session, err := s.passkeys.svc.FinishLogin(r.Context(), body.CeremonyID, body.Credential)
	if err != nil {
		s.ceremonyError(w, r, "login", err)
		return
	}
	writeJSON(w, http.StatusOK, toSessionResponse(session))
}

// ceremonyError traduce el fallo de una ceremonia.
func (s *Server) ceremonyError(w http.ResponseWriter, r *http.Request, what string, err error) {
	switch {
	case errors.Is(err, passkeys.ErrCeremonyNotFound):
		writeError(w, http.StatusBadRequest, "ceremony_expired",
			"la solicitud venció o ya se usó; vuelve a empezar")
	case errors.Is(err, passkeys.ErrAlreadyEnrolled):
		writeError(w, http.StatusConflict, "already_enrolled", "ya hay una passkey registrada")
	case errors.Is(err, passkeys.ErrCloned):
		// Para afuera, igual que cualquier passkey rechazada. Para adentro, un
		// incidente: hay dos copias de una clave privada que debería ser única.
		s.logger.ErrorContext(r.Context(), "passkeys: PASSKEY CLONADA, contador de firmas retrocedió",
			"ceremonia", what)
		writeError(w, http.StatusUnauthorized, "passkey_rejected", "no pudimos verificar tu passkey")
	case errors.Is(err, passkeys.ErrVerificationFailed), errors.Is(err, passkeys.ErrUnknownCredential):
		s.logger.WarnContext(r.Context(), "passkeys: verificación fallida", "ceremonia", what, "error", err)
		writeError(w, http.StatusUnauthorized, "passkey_rejected", "no pudimos verificar tu passkey")
	default:
		s.logger.ErrorContext(r.Context(), "passkeys: "+what, "error", err)
		writeError(w, http.StatusServiceUnavailable, "unavailable", "inténtalo de nuevo")
	}
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request, _ *Principal) {
	if err := s.passkeys.svc.Logout(r.Context(), bearerToken(r)); err != nil {
		s.logger.ErrorContext(r.Context(), "passkeys: cerrar sesión", "error", err)
		writeError(w, http.StatusServiceUnavailable, "unavailable", "inténtalo de nuevo")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func toSessionResponse(s *passkeys.Session) sessionResponse {
	return sessionResponse{Token: s.Token, CustomerID: s.CustomerID, ExpiresAt: s.ExpiresAt}
}

// handleListAccounts devuelve las cuentas del titular autenticado.
//
// Es lo primero que pide la app tras entrar: la persona dijo quién es con su
// passkey, no qué cuenta quiere ver.
func (s *Server) handleListAccounts(w http.ResponseWriter, r *http.Request, p *Principal) {
	accounts, err := s.core.ListCustomerAccounts(r.Context(), p.CustomerID)
	if err != nil {
		s.writeCoreError(w, r, err)
		return
	}
	out := make([]accountJSON, 0, len(accounts))
	for _, a := range accounts {
		out = append(out, accountJSON{ID: a.Id, Name: a.Name, Currency: a.Currency})
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": out})
}
