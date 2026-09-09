// Package mercatus expone la API REST del riel de pago para plataformas de
// agentes de IA.
//
// Es el canal de SOCIO, hermano del BFF y distinto de él: el BFF sirve a una
// persona con una sesión de dispositivo, este sirve a una plataforma con una
// credencial de máquina que actúa por muchas cuentas. Separarlos evita que las
// reglas de autorización de uno se filtren en el otro.
//
// Responsabilidades propias de esta capa:
//
//   - Autenticación OAuth2 y scopes.
//   - Propiedad de cuenta: una integración solo ve y mueve el dinero de las
//     sub-cuentas que abrió. Es la barrera contra usar un identificador ajeno.
//   - Traducción del vocabulario del core al del contrato: códigos HTTP y
//     motivos legibles por máquina, sin filtrar detalles internos.
//   - Identificadores públicos con prefijo (`acc_`, `auth_`).
//
// El dinero viaja SIEMPRE como entero en micras (10^-6). El contrato lo llama
// `amount` y son millonésimas: 1000 son $0.001.
package mercatus

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/aibank/aibank/clients/go/coreclient"
	"github.com/aibank/aibank/clients/go/telemetry"
	"github.com/aibank/aibank/services/oauth"
)

// Config son los parámetros del servicio.
type Config struct {
	// Sandbox habilita lo que solo tiene sentido en pruebas: abrir cuentas con
	// saldo inicial. En producción está apagado y el endpoint lo rechaza.
	Sandbox bool
	// ProductByCurrency mapea cada moneda al producto del catálogo bajo el que se
	// abren sus sub-cuentas.
	//
	// Es un mapa y no un producto único porque las cuentas son mono-moneda: un
	// agente que opera en dos monedas tiene dos cuentas, y cada una hereda las
	// reglas del producto de SU moneda. Una moneda sin producto configurado se
	// rechaza en vez de abrir la cuenta en la moneda equivocada.
	ProductByCurrency map[string]string
	// SandboxCashAccountID financia los saldos iniciales del sandbox.
	SandboxCashAccountID string
	ClientRate           int
	ClientBurst          int
	AccountRate          int
	AccountBurst         int
}

func (c Config) withDefaults() Config {
	if c.ClientRate == 0 {
		c.ClientRate = DefaultClientRate
	}
	if c.ClientBurst == 0 {
		c.ClientBurst = DefaultClientBurst
	}
	if c.AccountRate == 0 {
		c.AccountRate = DefaultAccountRate
	}
	if c.AccountBurst == 0 {
		c.AccountBurst = DefaultAccountBurst
	}
	if len(c.ProductByCurrency) == 0 {
		c.ProductByCurrency = map[string]string{"USD": "AGENT-USD"}
	}
	return c
}

type Server struct {
	core      *coreclient.Client
	oauth     *oauth.Server
	store     *oauth.Store
	cfg       Config
	log       *slog.Logger
	byClient  *Limiter
	byAccount *Limiter
}

func NewServer(core *coreclient.Client, oauthSrv *oauth.Server, store *oauth.Store, cfg Config, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	cfg = cfg.withDefaults()
	return &Server{
		core:      core,
		oauth:     oauthSrv,
		store:     store,
		cfg:       cfg,
		log:       log,
		byClient:  NewLimiter(cfg.ClientRate, cfg.ClientBurst),
		byAccount: NewLimiter(cfg.AccountRate, cfg.AccountBurst),
	}
}

// Handler arma el enrutador completo.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "sandbox": s.cfg.Sandbox})
	})

	// El endpoint del token no lleva Bearer, pero SÍ lleva límite de tasa: es el
	// único sitio donde se puede probar un secreto, y sin límite queda abierto a
	// fuerza bruta.
	mux.Handle("POST /oauth/token", s.rateLimitByRemote(http.HandlerFunc(s.oauth.Token)))

	mux.Handle("POST /v1/accounts", s.authorized(oauth.ScopeAccountsWrite, s.handleOpenAccount))
	mux.Handle("GET /v1/accounts/{accountID}/transactions",
		s.authorized(oauth.ScopePaymentsRead, s.handleTransactions))

	mux.Handle("POST /v1/authorizations", s.authorized(oauth.ScopePaymentsWrite, s.handleAuthorize))
	mux.Handle("POST /v1/authorizations/{id}/capture",
		s.authorized(oauth.ScopePaymentsWrite, s.handleCapture))
	mux.Handle("POST /v1/authorizations/{id}/refund",
		s.authorized(oauth.ScopePaymentsWrite, s.handleRefund))
	mux.Handle("GET /v1/authorizations/{id}", s.authorized(oauth.ScopePaymentsRead, s.handleGet))

	return telemetry.Middleware("mercatus", mux)
}

// handlerFunc recibe ya resuelta la identidad de la integración.
type handlerFunc func(http.ResponseWriter, *http.Request, *oauth.Claims)

// authorized encadena autenticación, scope y límite de tasa.
func (s *Server) authorized(scope string, next handlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, authErr := s.oauth.Authenticate(r, scope)
		if authErr != nil {
			writeError(w, authErr.Status, authErr.Code, authErr.Message)
			return
		}

		if allowed, retryAfter := s.byClient.Allow(claims.Subject); !allowed {
			s.tooManyRequests(w, retryAfter)
			return
		}

		next(w, r.WithContext(oauth.WithClaims(r.Context(), claims)), claims)
	})
}

// rateLimitByRemote limita por origen. Es lo único disponible antes de saber
// quién llama.
func (s *Server) rateLimitByRemote(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if allowed, retryAfter := s.byClient.Allow("ip:" + clientIP(r)); !allowed {
			s.tooManyRequests(w, retryAfter)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) tooManyRequests(w http.ResponseWriter, retryAfter time.Duration) {
	seconds := int(retryAfter.Seconds())
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	writeError(w, http.StatusTooManyRequests, CodeRateLimited,
		"demasiadas peticiones; reintenta pasado el tiempo de Retry-After")
}

// clientIP toma la dirección del par.
//
// Deliberadamente NO lee X-Forwarded-For: ese encabezado lo escribe quien llama y
// confiar en él permitiría saltarse el límite mandando una IP distinta en cada
// petición. Cuando haya un proxy delante, la dirección real tiene que llegar por
// un encabezado que ese proxy —y solo él— pueda fijar.
func clientIP(r *http.Request) string {
	host := r.RemoteAddr
	for i := len(host) - 1; i >= 0; i-- {
		if host[i] == ':' {
			return host[:i]
		}
	}
	return host
}
