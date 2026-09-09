// Package oauth implementa el servidor de credenciales de cliente que autentica
// a las integraciones de socios.
//
// Es `client_credentials` de OAuth2 y no una API key estática por dos razones que
// importan en la práctica: una credencial se revoca sin tener que rotar un
// secreto de larga vida en el otro extremo, y un token filtrado deja de servir
// solo en una hora en lugar de quedar activo hasta que alguien lo note.
package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"
)

// Scopes conocidos.
const (
	ScopePaymentsRead  = "payments:read"
	ScopePaymentsWrite = "payments:write"
	ScopeAccountsWrite = "accounts:write"
)

// Server expone `POST /oauth/token`.
type Server struct {
	store  *Store
	issuer *Issuer
	log    *slog.Logger
}

func NewServer(store *Store, issuer *Issuer, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{store: store, issuer: issuer, log: log}
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
	Scope       string `json:"scope"`
}

// errorResponse sigue el formato de RFC 6749 §5.2, que es lo que espera
// cualquier cliente OAuth2 genérico.
type errorResponse struct {
	Error       string `json:"error"`
	Description string `json:"error_description,omitempty"`
}

// Token atiende la emisión de tokens.
func (s *Server) Token(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOAuthError(w, http.StatusMethodNotAllowed, "invalid_request", "usa POST")
		return
	}
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "cuerpo no es form-urlencoded")
		return
	}

	if grant := r.PostFormValue("grant_type"); grant != "client_credentials" {
		writeOAuthError(w, http.StatusBadRequest, "unsupported_grant_type",
			"solo se admite client_credentials")
		return
	}

	clientID, secret := credentials(r)
	if clientID == "" || secret == "" {
		writeOAuthError(w, http.StatusUnauthorized, "invalid_client",
			"faltan client_id o client_secret")
		return
	}

	client, err := s.store.Authenticate(r.Context(), clientID, secret)
	if err != nil {
		// La respuesta es la MISMA para cliente inexistente, secreto incorrecto y
		// cliente desactivado. Distinguirlos convertiría el endpoint en un oráculo
		// que confirma qué client_id existen.
		s.log.WarnContext(r.Context(), "autenticación de cliente rechazada",
			"client_id", clientID, "motivo", err.Error())
		writeOAuthError(w, http.StatusUnauthorized, "invalid_client", "credenciales inválidas")
		return
	}

	granted, err := GrantScopes(client, strings.Fields(r.PostFormValue("scope")))
	if err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_scope", err.Error())
		return
	}

	token, claims, err := s.issuer.Issue(client.ID, granted, uuid.NewString())
	if err != nil {
		s.log.ErrorContext(r.Context(), "no se pudo emitir el token", "error", err)
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "")
		return
	}

	s.log.InfoContext(r.Context(), "token emitido",
		"client_id", client.ID, "jti", claims.TokenID, "scopes", granted)

	// Un token no se cachea nunca, ni siquiera en un proxy intermedio.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	writeJSON(w, http.StatusOK, tokenResponse{
		AccessToken: token,
		TokenType:   "Bearer",
		ExpiresIn:   int(TokenTTL.Seconds()),
		Scope:       strings.Join(granted, " "),
	})
}

// credentials acepta las credenciales por Basic auth o en el cuerpo.
//
// El RFC prefiere Basic; casi todos los clientes reales mandan el cuerpo. Admitir
// las dos evita una fricción de integración que no protege de nada.
func credentials(r *http.Request) (string, string) {
	if id, secret, ok := r.BasicAuth(); ok && id != "" {
		return id, secret
	}
	return r.PostFormValue("client_id"), r.PostFormValue("client_secret")
}

// ---------------------------------------------------------------- middleware

type claimsKey struct{}

// ClaimsFrom recupera las claims del token que autorizó la petición.
func ClaimsFrom(ctx context.Context) (*Claims, bool) {
	c, ok := ctx.Value(claimsKey{}).(*Claims)
	return c, ok
}

// AuthError describe por qué se rechazó una petición autenticada.
type AuthError struct {
	Code    string
	Message string
	Status  int
}

func (e *AuthError) Error() string { return e.Message }

// Authenticate verifica el Bearer del encabezado y exige un scope.
//
// Devuelve un error tipado en vez de escribir la respuesta: cada API expone su
// propio formato de error —el contrato con Mercatus tiene el suyo— y esta capa no
// puede imponer uno.
func (s *Server) Authenticate(r *http.Request, requiredScope string) (*Claims, *AuthError) {
	token := bearerToken(r)
	if token == "" {
		return nil, &AuthError{
			Code:    "invalid_credential",
			Message: "falta el encabezado Authorization: Bearer",
			Status:  http.StatusUnauthorized,
		}
	}

	claims, err := s.issuer.Verify(token)
	if err != nil {
		message := "token inválido"
		if errors.Is(err, ErrTokenExpired) {
			// Distinguir el vencimiento SÍ es útil y no filtra nada: le dice al
			// cliente que pida otro token en vez de revisar sus credenciales.
			message = "token expirado"
		}
		return nil, &AuthError{
			Code:    "invalid_credential",
			Message: message,
			Status:  http.StatusUnauthorized,
		}
	}

	if requiredScope != "" && !claims.HasScope(requiredScope) {
		return nil, &AuthError{
			Code:    "insufficient_scope",
			Message: "el token no incluye el scope " + requiredScope,
			Status:  http.StatusForbidden,
		}
	}

	return claims, nil
}

// WithClaims deja las claims en el contexto de la petición.
func WithClaims(ctx context.Context, claims *Claims) context.Context {
	return context.WithValue(ctx, claimsKey{}, claims)
}

func bearerToken(r *http.Request) string {
	header := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(header[len(prefix):])
}

// ---------------------------------------------------------------- respuestas

func writeOAuthError(w http.ResponseWriter, status int, code, description string) {
	if status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Basic realm="aibank"`)
	}
	writeJSON(w, status, errorResponse{Error: code, Description: description})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
