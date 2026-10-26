package bff

import (
	"errors"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aibank/aibank/adapters/kyc"
	"github.com/aibank/aibank/services/onboarding"
	"github.com/aibank/aibank/services/ratelimit"
	"github.com/google/uuid"
)

// El alta de clientes, desde el canal del titular.
//
// Es la única parte del canal SIN sesión: quien se da de alta todavía no es
// cliente. Eso la convierte en la puerta más expuesta del banco, y además cada
// alta encarga verificaciones de identidad que se le pagan al proveedor. Tres
// defensas:
//
//   - **Límite por origen.** Sin él, cualquiera podría encargarnos verificaciones
//     en bucle y pasarnos la factura.
//   - **La referencia del alta la genera el dispositivo** (un UUID aleatorio) y es
//     lo único que permite consultarla después. Funciona como un token: quien no
//     la tiene no puede ver si otra persona fue aprobada.
//   - **Nada del motivo de una decisión sale de aquí.** Una coincidencia en listas
//     de sanciones no se le puede revelar al interesado —la normativa contra el
//     lavado lo prohíbe expresamente—, así que la respuesta es la misma para una
//     foto borrosa que para una coincidencia en la OFAC: "lo estamos revisando".
//
// Este es el NIVEL 1 del alta por niveles (doc 14): datos del DNI contrastados
// con la fuente oficial y una cuenta de topes bajos. El nivel 2 —prueba de vida
// con la cámara— sube los topes, y necesita el SDK nativo del proveedor y que el
// core sepa cambiar el producto de una cuenta. Ninguna de las dos cosas existe
// todavía.

// referenceHeader lleva la referencia del alta en la consulta de estado. Va en
// una cabecera y no en la URL: las URL acaban en los logs de cualquier proxy.
const referenceHeader = "X-Onboarding-Reference"

// Valores por defecto del límite: una persona real envía el formulario una o
// dos veces. Ráfaga de 5 por reintentos de red; 1 por segundo sostenido.
const (
	defaultOnboardingRate  = 1
	defaultOnboardingBurst = 5
	// Mayoría de edad en Perú.
	adultAge = 18
)

var dniPattern = regexp.MustCompile(`^[0-9]{8}$`)

type onboardingConfig struct {
	service     *onboarding.Service
	store       *onboarding.Store
	productCode string
	limiter     *ratelimit.Limiter
	now         func() time.Time
}

// OnboardingOption ajusta el alta. Existe para que las pruebas puedan fijar el
// reloj y el límite.
type OnboardingOption func(*onboardingConfig)

// WithOnboardingLimit cambia el límite por origen.
func WithOnboardingLimit(rate, burst int) OnboardingOption {
	return func(c *onboardingConfig) { c.limiter = ratelimit.NewLimiter(rate, burst) }
}

// WithOnboardingClock fija el reloj con el que se calcula la edad.
func WithOnboardingClock(now func() time.Time) OnboardingOption {
	return func(c *onboardingConfig) { c.now = now }
}

// WithOnboarding habilita el alta en el canal. `productCode` es el producto con
// el que se abren las cuentas de nivel 1.
func (s *Server) WithOnboarding(service *onboarding.Service, store *onboarding.Store, productCode string, opts ...OnboardingOption) *Server {
	cfg := &onboardingConfig{
		service:     service,
		store:       store,
		productCode: productCode,
		limiter:     ratelimit.NewLimiter(defaultOnboardingRate, defaultOnboardingBurst),
		now:         time.Now,
	}
	for _, opt := range opts {
		opt(cfg)
	}
	s.onboarding = cfg
	return s
}

type onboardingRequest struct {
	// Reference la genera el dispositivo y la CONSERVA en cada reintento: es lo
	// que hace que reenviar el formulario tras un corte de red no abra un segundo
	// expediente ni vuelva a pagar la verificación.
	Reference      string `json:"reference"`
	DocumentNumber string `json:"document_number"`
	FirstNames     string `json:"first_names"`
	LastNames      string `json:"last_names"`
	// DateOfBirth en formato AAAA-MM-DD.
	DateOfBirth string `json:"date_of_birth"`
}

type onboardingResponse struct {
	Reference string `json:"reference"`
	// Status: approved, in_review, rejected o in_progress.
	Status string `json:"status"`
	// Solo cuando la cuenta está abierta.
	CustomerID string `json:"customer_id,omitempty"`
	AccountID  string `json:"account_id,omitempty"`
}

func (s *Server) handleSubmitOnboarding(w http.ResponseWriter, r *http.Request) {
	if ok, wait := s.onboarding.limiter.Allow(clientIP(r)); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeError(w, http.StatusTooManyRequests, "rate_limited", "demasiados intentos; espera un momento")
		return
	}

	var req onboardingRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	applicant, code, message := s.validateApplicant(req)
	if code != "" {
		writeError(w, http.StatusBadRequest, code, message)
		return
	}

	externalRef := "app:" + req.Reference

	// Una misma referencia es UNA persona. Si llega con otros datos no se
	// reutiliza el expediente: se rechaza, porque reanudar el de otra persona
	// abriría su cuenta con los datos de alguien distinto.
	existing, err := s.onboarding.store.ByExternalRef(r.Context(), externalRef)
	if err == nil && !sameApplicant(existing.Applicant, applicant) {
		writeError(w, http.StatusConflict, "reference_conflict",
			"esa referencia ya se usó con otros datos; genera una nueva")
		return
	}

	app, err := s.onboarding.service.Submit(r.Context(), onboarding.SubmitRequest{
		ExternalRef: externalRef,
		// Solo se usa si el expediente es nuevo; si ya existía, conserva el suyo.
		CustomerID:  uuid.NewString(),
		ProductCode: s.onboarding.productCode,
		// Derivado de la referencia y no aleatorio: si el alta se corta entre
		// abrir la cuenta y registrarlo, el reintento tiene que encontrar ESA
		// cuenta. Un código nuevo en cada intento abriría una segunda.
		AccountCode: "ONB-" + strings.ToUpper(strings.ReplaceAll(req.Reference, "-", "")),
		Applicant:   applicant,
	})
	if err != nil {
		if errors.Is(err, kyc.ErrProviderUnavailable) {
			s.logger.WarnContext(r.Context(), "alta: proveedor de identidad no disponible", "error", err)
		} else {
			s.logger.ErrorContext(r.Context(), "alta: no se pudo avanzar", "error", err)
		}
		// Reintentable en los dos casos: el expediente quedó donde estaba, y
		// reenviar con la misma referencia lo retoma sin repetir lo ya hecho.
		writeError(w, http.StatusServiceUnavailable, "unavailable",
			"no pudimos completar la verificación; vuelve a intentarlo")
		return
	}

	writeJSON(w, http.StatusOK, toOnboardingResponse(req.Reference, app))
}

func (s *Server) handleOnboardingStatus(w http.ResponseWriter, r *http.Request) {
	reference := r.Header.Get(referenceHeader)
	if _, err := uuid.Parse(reference); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_reference", "falta o no es válida "+referenceHeader)
		return
	}

	app, err := s.onboarding.store.ByExternalRef(r.Context(), "app:"+reference)
	if err != nil {
		// Mismo 404 exista o no un error de otra índole detrás: no se confirma
		// la existencia de expedientes ajenos.
		writeError(w, http.StatusNotFound, "not_found", "no hay un alta con esa referencia")
		return
	}
	writeJSON(w, http.StatusOK, toOnboardingResponse(reference, app))
}

// validateApplicant comprueba lo que se puede comprobar sin el proveedor.
//
// La app valida lo mismo para dar el aviso en el campo, pero la verdad está
// aquí: una app antigua o manipulada no puede saltarse estas reglas.
func (s *Server) validateApplicant(req onboardingRequest) (kyc.Applicant, string, string) {
	if _, err := uuid.Parse(req.Reference); err != nil {
		return kyc.Applicant{}, "invalid_reference", "reference debe ser un UUID generado por el dispositivo"
	}
	if !dniPattern.MatchString(req.DocumentNumber) {
		return kyc.Applicant{}, "invalid_document", "el DNI tiene 8 dígitos"
	}

	first := strings.Join(strings.Fields(req.FirstNames), " ")
	last := strings.Join(strings.Fields(req.LastNames), " ")
	if first == "" || last == "" || len(first) > 100 || len(last) > 100 {
		return kyc.Applicant{}, "invalid_name", "faltan los nombres o los apellidos"
	}

	birth, err := time.Parse("2006-01-02", req.DateOfBirth)
	if err != nil || birth.Year() < 1900 {
		return kyc.Applicant{}, "invalid_date", "la fecha de nacimiento no es válida"
	}
	if ageOn(birth, s.onboarding.now()) < adultAge {
		return kyc.Applicant{}, "underage", "hay que ser mayor de edad para abrir una cuenta"
	}

	return kyc.Applicant{
		FullName:       first + " " + last,
		DocumentType:   "DNI",
		DocumentNumber: req.DocumentNumber,
		DateOfBirth:    birth,
		CountryCode:    "PE",
	}, "", ""
}

// ageOn calcula la edad cumplida en una fecha.
func ageOn(birth, at time.Time) int {
	age := at.Year() - birth.Year()
	if at.Month() < birth.Month() || (at.Month() == birth.Month() && at.Day() < birth.Day()) {
		age--
	}
	return age
}

func sameApplicant(a, b kyc.Applicant) bool {
	return a.DocumentNumber == b.DocumentNumber &&
		a.DateOfBirth.Format("2006-01-02") == b.DateOfBirth.Format("2006-01-02") &&
		strings.EqualFold(a.FullName, b.FullName)
}

// toOnboardingResponse traduce el expediente a lo que puede ver el interesado.
//
// Deliberadamente NO incluye `DecisionReason`: ver la cabecera de este archivo.
func toOnboardingResponse(reference string, app *onboarding.Application) onboardingResponse {
	resp := onboardingResponse{Reference: reference}
	switch app.Status {
	case onboarding.StatusApproved:
		resp.Status = "approved"
		resp.CustomerID = app.CustomerID
		resp.AccountID = app.AccountID
	case onboarding.StatusManualReview:
		resp.Status = "in_review"
	case onboarding.StatusRejected:
		resp.Status = "rejected"
	default:
		resp.Status = "in_progress"
	}
	return resp
}

// clientIP identifica el origen para el límite.
//
// Usa la dirección de la conexión, no X-Forwarded-For: esa cabecera la escribe
// quien quiera, y confiar en ella permitiría saltarse el límite inventándose una
// IP en cada petición. Detrás de un proxy hay que configurarla desde el proxy.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
