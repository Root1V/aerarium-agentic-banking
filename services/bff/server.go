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
// El dinero viaja SIEMPRE como entero en micras (10^-6) más la moneda. Nunca
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
	"github.com/aibank/aibank/services/agents"
	"github.com/aibank/aibank/services/credit"
	"github.com/aibank/aibank/services/forecast"
	"github.com/aibank/aibank/services/oauth"
	"github.com/aibank/aibank/services/sessions"
)

// Server es la API del canal móvil.
type Server struct {
	core   *coreclient.Client
	auth   Authenticator
	logger *slog.Logger
	// consents habilita las pantallas de permiso del modelo B. Es opcional: sin
	// él la app funciona igual, solo que sin poder otorgar ni revocar permisos a
	// plataformas. Así el canal móvil no queda acoplado a que exista una
	// integración de socio.
	consents *oauth.Store
	// forecast proyecta el saldo hasta el próximo ingreso. Opcional: necesita el
	// motor de riesgo, igual que la tarjeta.
	forecast *forecast.Service
	// agents dice qué agente hizo cada pago con un permiso y qué compró. Sin él
	// el historial agrupa por plataforma, sin el detalle.
	agents *agents.Store
	// onboarding habilita el alta de clientes. Opcional, por la misma razón.
	onboarding *onboardingConfig
	// passkeys habilita el login real y sus ceremonias.
	passkeys *passkeyConfig
	// devices habilita vincular teléfonos y entrar con ellos.
	devices *deviceConfig
	// sessions habilita cerrar sesión en el banco.
	sessions *sessions.Store
	// control decide las operaciones del titular y pide confirmar con el
	// teléfono las de riesgo.
	control *controlConfig
	// sandbox habilita el saldo de prueba. Solo en el sandbox.
	sandbox *sandboxConfig
	// aliases habilita el directorio de celulares.
	aliases *aliasConfig
	// credit habilita la tarjeta de crédito.
	credit *credit.Service
	// timeZone es la zona en la que se cuenta "hoy" y "este mes" para el
	// titular. Una sola mientras el banco opera en un país; con más de uno,
	// saldrá del perfil del cliente.
	timeZone string
}

// DefaultTimeZone es la del país ancla.
const DefaultTimeZone = "America/Lima"

func NewServer(core *coreclient.Client, auth Authenticator, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{core: core, auth: auth, logger: logger, timeZone: DefaultTimeZone}
}

// WithConsents habilita la gestión de permisos delegados en la app.
func (s *Server) WithConsents(store *oauth.Store) *Server {
	s.consents = store
	return s
}

// WithAgents habilita mostrar, en cada pago de un agente, quién lo hizo y qué
// compró.
func (s *Server) WithAgents(store *agents.Store) *Server {
	s.agents = store
	return s
}

// Handler arma el enrutador con la autenticación ya aplicada.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	mux.Handle("GET /v1/accounts", s.requireAuth(s.handleListAccounts))
	mux.Handle("GET /v1/home", s.requireAuth(s.handleHome))
	mux.Handle("GET /v1/accounts/{accountID}/balance", s.requireAuth(s.handleBalance))
	mux.Handle("GET /v1/accounts/{accountID}/movements", s.requireAuth(s.handleMovements))
	if s.forecast != nil {
		mux.Handle("GET /v1/accounts/{accountID}/forecast", s.requireAuth(s.handleForecast))
	}
	mux.Handle("POST /v1/transfers", s.requireAuth(s.handleTransfer))
	mux.Handle("GET /v1/payees/recent", s.requireAuth(s.handleRecentPayees))

	// Permisos delegados (modelo B). Solo se montan si el servicio los tiene
	// configurados: una ruta que devolvería siempre error es peor que una ruta
	// que no existe.
	if s.consents != nil {
		mux.Handle("GET /v1/consent-requests/{handoff}", s.requireAuth(s.handleConsentPrompt))
		mux.Handle("POST /v1/consent-requests/{handoff}/approve", s.requireAuth(s.handleApproveConsent))
		mux.Handle("POST /v1/consent-requests/{handoff}/reject", s.requireAuth(s.handleRejectConsent))
		mux.Handle("GET /v1/mandates", s.requireAuth(s.handleListMandates))
		mux.Handle("POST /v1/mandates/{mandateID}/revoke", s.requireAuth(s.handleRevokeMandate))
	}

	if s.passkeys != nil {
		s.passkeyRoutes(mux)
	}
	if s.devices != nil {
		s.deviceRoutes(mux)
	}
	if s.sessions != nil {
		mux.Handle("POST /v1/session/logout", s.requireAuth(s.handleLogout))
	}
	if s.sandbox != nil {
		s.sandboxRoutes(mux)
	}
	if s.aliases != nil {
		s.aliasRoutes(mux)
	}
	if s.credit != nil {
		s.creditRoutes(mux)
	}

	// El alta va SIN requireAuth: quien se da de alta todavía no tiene sesión.
	// Sus defensas están en onboarding.go.
	if s.onboarding != nil {
		mux.HandleFunc("POST /v1/onboarding", s.handleSubmitOnboarding)
		mux.HandleFunc("GET /v1/onboarding", s.handleOnboardingStatus)
	}

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
	// AmountMicros es entero en micras: 1_000_000 micras = 1 unidad.
	AmountMicros int64  `json:"amount_micros"`
	Currency     string `json:"currency"`
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
	Sign        int    `json:"sign"`
	Kind        string `json:"kind"`
	Description string `json:"description"`
	// Merchant es el nombre registrado de la plataforma, en los pagos hechos
	// con un permiso. Es lo que deja al historial agrupar "120 pagos a
	// Mercatus" en vez de mostrar 120 filas anónimas.
	Merchant string `json:"merchant,omitempty"`
	// AgentName y Purchase: qué agente de esa plataforma pagó y qué compró, tal
	// como lo declaró al pagar (AE-10). Ausentes en los pagos de antes.
	AgentName string `json:"agent_name,omitempty"`
	Purchase  string `json:"purchase,omitempty"`
	// Counterparty es la otra persona de una transferencia: a quién se le envió
	// o de quién se recibió. Es lo que deja al historial decir "A Juan Pérez" en
	// vez de "Transferencia".
	Counterparty *accountJSON `json:"counterparty,omitempty"`
	PostedAt     time.Time    `json:"posted_at"`
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
	case errors.Is(err, coreclient.ErrCreditLimitExceeded):
		writeError(w, http.StatusUnprocessableEntity, "credit_limit_exceeded", "supera lo disponible en tu tarjeta")
	case errors.Is(err, coreclient.ErrCreditLineInactive):
		writeError(w, http.StatusUnprocessableEntity, "credit_line_inactive", "tu tarjeta está bloqueada; puedes pagarla")
	case errors.Is(err, coreclient.ErrOverpayment):
		writeError(w, http.StatusUnprocessableEntity, "overpayment", "el pago supera lo que debes")
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
