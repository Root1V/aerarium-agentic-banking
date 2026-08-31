// Package bff expone la API que consume la app móvil.
//
// Responsabilidades propias de esta capa, y de ninguna otra:
//
//   - Autorización por cliente: cada respuesta contiene solo lo que el titular
//     autenticado puede ver. El core no sabe quién pregunta; el BFF sí.
//   - Agregación: la pantalla principal se arma en una sola llamada, porque el
//     móvil suele estar en una red mala y cada viaje extra se nota.
//   - Traducción de errores: al cliente le llega un motivo accionable, nunca un
//     detalle interno del core o de un proveedor.
//
// El dinero viaja SIEMPRE como entero en unidades menores más la moneda. Nunca
// como número decimal: JSON no distingue enteros de flotantes y el consumidor
// acabaría haciendo aritmética binaria con saldos.
package bff

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/aibank/aibank/clients/go/coreclient"
	"github.com/aibank/aibank/clients/go/telemetry"
)

// Server es la API del canal móvil.
type Server struct {
	core   *coreclient.Client
	auth   Authenticator
	logger *slog.Logger
}

func NewServer(core *coreclient.Client, auth Authenticator, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{core: core, auth: auth, logger: logger}
}

// Handler arma el enrutador con la autenticación ya aplicada.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	mux.Handle("GET /v1/home", s.requireAuth(s.handleHome))
	mux.Handle("GET /v1/accounts/{accountID}/balance", s.requireAuth(s.handleBalance))
	mux.Handle("GET /v1/accounts/{accountID}/movements", s.requireAuth(s.handleMovements))
	mux.Handle("POST /v1/transfers", s.requireAuth(s.handleTransfer))

	// El middleware recupera el contexto de traza entrante y abre un span por
	// petición. Desde aquí viaja al core por la metadata gRPC y, si la operación
	// encola un evento, sobrevive al salto asíncrono del outbox.
	return telemetry.Middleware("bff", mux)
}

// requireAuth resuelve el token antes de ejecutar el handler.
func (s *Server) requireAuth(next func(http.ResponseWriter, *http.Request, *Principal)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if token == "" {
			writeError(w, http.StatusUnauthorized, "unauthenticated", "falta el token de sesión")
			return
		}

		principal, err := s.auth.Authenticate(r.Context(), token)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthenticated", "sesión inválida o expirada")
			return
		}

		next(w, r.WithContext(withPrincipal(r.Context(), principal)), principal)
	})
}

// ---------------------------------------------------------------- respuestas

// moneyJSON es la representación de un importe hacia el cliente.
type moneyJSON struct {
	// AmountMinor es entero en unidades menores (centavos).
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
}

type accountJSON struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Currency string `json:"currency"`
}

type movementJSON struct {
	ID     string    `json:"id"`
	Cursor string    `json:"cursor"`
	Amount moneyJSON `json:"amount"`
	// Sign es +1 si el movimiento suma al saldo del cliente y -1 si resta.
	// Se calcula aquí para que la app no tenga que conocer la contabilidad.
	Sign        int       `json:"sign"`
	Kind        string    `json:"kind"`
	Description string    `json:"description"`
	PostedAt    time.Time `json:"posted_at"`
}

type errorJSON struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorJSON{Code: code, Message: message})
}

// writeCoreError traduce un error del core a una respuesta para el cliente.
//
// Nunca se filtra el mensaje interno: puede contener identificadores de cuentas
// internas o detalles del proveedor. El cliente recibe un motivo accionable.
func (s *Server) writeCoreError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrForbidden), errors.Is(err, coreclient.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "no encontrado")
	case errors.Is(err, coreclient.ErrInsufficientFunds):
		writeError(w, http.StatusUnprocessableEntity, "insufficient_funds", "saldo insuficiente")
	case errors.Is(err, coreclient.ErrBalanceCapExceeded):
		writeError(w, http.StatusUnprocessableEntity, "balance_cap_exceeded", "se superaría el límite de saldo de la cuenta")
	case errors.Is(err, coreclient.ErrTransactionCapExceeded):
		writeError(w, http.StatusUnprocessableEntity, "transaction_cap_exceeded", "el monto supera el límite por operación")
	case errors.Is(err, coreclient.ErrIdempotencyConflict):
		writeError(w, http.StatusConflict, "idempotency_conflict", "esa clave ya se usó con otra operación")
	case errors.Is(err, coreclient.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_request", "la operación no es válida")
	default:
		// Un fallo de infraestructura se registra completo del lado del servidor
		// y se responde de forma genérica.
		// El identificador de traza va en el log: es el puente entre "algo falló"
		// y la traza que cuenta qué pasó exactamente.
		s.logger.ErrorContext(r.Context(), "fallo al hablar con el core",
			"error", err, "path", r.URL.Path, "trace_id", telemetry.TraceID(r.Context()))
		writeError(w, http.StatusServiceUnavailable, "unavailable", "servicio no disponible, intenta de nuevo")
	}
}
