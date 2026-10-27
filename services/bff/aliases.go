package bff

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"strconv"

	"github.com/aibank/aibank/services/aliases"
	"github.com/aibank/aibank/services/ratelimit"
)

// Enviar a un celular: registrar el número propio con un código por SMS y
// buscar a quién pertenece un número.

const (
	defaultLookupRate  = 1
	defaultLookupBurst = 10
)

type aliasConfig struct {
	svc *aliases.Service
	// sandboxCodes, solo en el sandbox: el código "enviado" vuelve en la
	// respuesta porque no hay SMS de verdad.
	sandboxCodes *aliases.SandboxSender
	// lookups limita las búsquedas por cliente: sin tope, el directorio serviría
	// para recorrer números y averiguar quién es cliente.
	lookups *ratelimit.Limiter
}

// AliasOption ajusta el directorio de celulares.
type AliasOption func(*aliasConfig)

// WithLookupLimit cambia el tope de búsquedas por cliente.
func WithLookupLimit(rate, burst int) AliasOption {
	return func(c *aliasConfig) { c.lookups = ratelimit.NewLimiter(rate, burst) }
}

// WithAliases habilita el directorio de celulares. `sandboxCodes` solo en el
// sandbox; en cualquier otro entorno, nil.
func (s *Server) WithAliases(svc *aliases.Service, sandboxCodes *aliases.SandboxSender, opts ...AliasOption) *Server {
	cfg := &aliasConfig{
		svc:          svc,
		sandboxCodes: sandboxCodes,
		lookups:      ratelimit.NewLimiter(defaultLookupRate, defaultLookupBurst),
	}
	for _, opt := range opts {
		opt(cfg)
	}
	s.aliases = cfg
	return s
}

func (s *Server) aliasRoutes(mux *http.ServeMux) {
	mux.Handle("GET /v1/aliases/phone", s.requireAuth(s.handleMyPhone))
	mux.Handle("POST /v1/aliases/phone", s.requireAuth(s.handleStartPhone))
	mux.Handle("POST /v1/aliases/phone/verify", s.requireAuth(s.handleVerifyPhone))
	mux.Handle("DELETE /v1/aliases/phone", s.requireAuth(s.handleRemovePhone))
	mux.Handle("GET /v1/directory/phone/{phone}", s.requireAuth(s.handleLookupPhone))
}

func (s *Server) handleMyPhone(w http.ResponseWriter, r *http.Request, p *Principal) {
	alias, err := s.aliases.svc.Mine(r.Context(), p.CustomerID)
	if errors.Is(err, aliases.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "no tienes un número registrado")
		return
	}
	if err != nil {
		s.aliasError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"phone": alias.Phone, "account_id": alias.AccountID})
}

func (s *Server) handleStartPhone(w http.ResponseWriter, r *http.Request, p *Principal) {
	var body struct {
		AccountID string `json:"account_id"`
		Phone     string `json:"phone"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	// Lo que llegue al número entra a esta cuenta: tiene que ser del titular.
	if _, err := s.authorizeAccount(r.Context(), p, body.AccountID); err != nil {
		s.writeCoreError(w, r, err)
		return
	}
	v, err := s.aliases.svc.StartVerification(r.Context(), p.CustomerID, body.AccountID, body.Phone)
	if err != nil {
		s.aliasError(w, r, err)
		return
	}
	resp := map[string]any{"verification_id": v.ID, "phone": v.Phone, "expires_at": v.ExpiresAt}
	if s.aliases.sandboxCodes != nil {
		resp["sandbox_code"] = s.aliases.sandboxCodes.LastCode(v.Phone)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleVerifyPhone(w http.ResponseWriter, r *http.Request, p *Principal) {
	var body struct {
		VerificationID string `json:"verification_id"`
		Code           string `json:"code"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	phone, err := s.aliases.svc.Verify(r.Context(), p.CustomerID, body.VerificationID, body.Code)
	if err != nil {
		s.aliasError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"phone": phone})
}

func (s *Server) handleRemovePhone(w http.ResponseWriter, r *http.Request, p *Principal) {
	if err := s.aliases.svc.Remove(r.Context(), p.CustomerID); err != nil {
		s.aliasError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleLookupPhone dice a quién llega lo que se envía a un número: su cuenta y
// el nombre a medias. Con tope por cliente.
func (s *Server) handleLookupPhone(w http.ResponseWriter, r *http.Request, p *Principal) {
	if ok, wait := s.aliases.lookups.Allow(p.CustomerID); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeError(w, http.StatusTooManyRequests, "rate_limited", "demasiadas búsquedas; espera un momento")
		return
	}
	alias, err := s.aliases.svc.Resolve(r.Context(), r.PathValue("phone"))
	if err != nil {
		s.aliasError(w, r, err)
		return
	}
	account, err := s.core.GetAccountByID(r.Context(), alias.AccountID)
	if err != nil {
		s.writeCoreError(w, r, err)
		return
	}
	// El número propio: enviarse a uno mismo no es una transferencia. Se dice
	// aquí, al buscar, y no después como un error genérico al enviar.
	if account.OwnerId == p.CustomerID {
		writeError(w, http.StatusConflict, "own_phone", "ese es tu número")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"phone":   alias.Phone,
		"account": accountJSON{ID: account.Id, Name: aliases.MaskName(account.Name), Currency: account.Currency},
	})
}

func (s *Server) aliasError(w http.ResponseWriter, r *http.Request, err error) {
	var wrong *aliases.WrongCodeError
	switch {
	case errors.As(err, &wrong):
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"code": "code_incorrect", "message": "código incorrecto", "remaining_attempts": wrong.Remaining,
		})
	case errors.Is(err, aliases.ErrInvalidPhone):
		writeError(w, http.StatusBadRequest, "invalid_phone", "escribe un celular de 9 dígitos")
	case errors.Is(err, aliases.ErrNotFound):
		writeError(w, http.StatusNotFound, "phone_not_found", "ese número no tiene cuenta en el banco")
	case errors.Is(err, aliases.ErrVerificationNotFound):
		writeError(w, http.StatusBadRequest, "ceremony_expired", "el código venció; pide otro")
	case errors.Is(err, aliases.ErrTooManyAttempts):
		writeError(w, http.StatusBadRequest, "code_locked", "demasiados intentos; pide otro código")
	default:
		s.logger.ErrorContext(r.Context(), "directorio de celulares", "error", err)
		writeError(w, http.StatusServiceUnavailable, "unavailable", "inténtalo de nuevo")
	}
}

// sandboxPhone es un celular de prueba para el destinatario del sandbox.
func sandboxPhone() string { return fmt.Sprintf("9%08d", rand.IntN(100_000_000)) }
