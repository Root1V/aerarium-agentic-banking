package bff

import (
	"encoding/base64"
	"errors"
	"net/http"
	"slices"
	"strconv"

	"github.com/aibank/aibank/services/devices"
	"github.com/aibank/aibank/services/ratelimit"
	"github.com/aibank/aibank/services/sessions"
)

// Teléfonos vinculados: la capa diaria del login. Vincular el primero tras el
// alta, entrar con la cara o con la clave, y desvincular.
//
// Todos los binarios —claves públicas, firmas, el desafío— viajan en base64url
// sin relleno, como en WebAuthn.

type deviceConfig struct {
	svc     *devices.Service
	limiter *ratelimit.Limiter
}

// DeviceOption ajusta los teléfonos vinculados.
type DeviceOption func(*deviceConfig)

// WithDeviceLimit cambia el límite por origen.
func WithDeviceLimit(rate, burst int) DeviceOption {
	return func(c *deviceConfig) { c.limiter = ratelimit.NewLimiter(rate, burst) }
}

// WithDevices habilita vincular teléfonos y entrar con ellos.
func (s *Server) WithDevices(svc *devices.Service, opts ...DeviceOption) *Server {
	cfg := &deviceConfig{
		svc:     svc,
		limiter: ratelimit.NewLimiter(defaultPasskeyRate, defaultPasskeyBurst),
	}
	for _, opt := range opts {
		opt(cfg)
	}
	s.devices = cfg
	return s
}

type deviceChallengeResponse struct {
	ChallengeID string `json:"challenge_id"`
	// Payload son los bytes exactos que el teléfono firma.
	Payload string `json:"payload"`
}

type deviceSessionResponse struct {
	sessionResponse
	DeviceID string `json:"device_id,omitempty"`
}

func (s *Server) deviceRoutes(mux *http.ServeMux) {
	limited := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if ok, wait := s.devices.limiter.Allow(clientIP(r)); !ok {
				w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
				writeError(w, http.StatusTooManyRequests, "rate_limited", "demasiados intentos; espera un momento")
				return
			}
			h(w, r)
		}
	}

	mux.HandleFunc("POST /v1/devices/registration/options", limited(s.handleDeviceRegistrationOptions))
	mux.HandleFunc("POST /v1/devices/registration", limited(s.handleDeviceRegistration))
	mux.HandleFunc("POST /v1/devices/login/options", limited(s.handleDeviceLoginOptions))
	mux.HandleFunc("POST /v1/devices/login", limited(s.handleDeviceLogin))
	mux.Handle("DELETE /v1/devices/{id}", s.requireAuth(s.handleRevokeDevice))
}

func (s *Server) handleDeviceRegistrationOptions(w http.ResponseWriter, r *http.Request) {
	c, err := s.devices.svc.BeginRegistration(r.Context())
	if err != nil {
		s.deviceError(w, r, "desafío de vinculación", err)
		return
	}
	writeJSON(w, http.StatusOK, toChallengeResponse(c))
}

// handleDeviceRegistration vincula un teléfono.
//
// Con la referencia del alta, el primero. Con una sesión —tras entrar con la
// passkey en un teléfono nuevo—, uno más para ese cliente. Si llegan las dos,
// manda la sesión: ya dice quién es.
func (s *Server) handleDeviceRegistration(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ChallengeID         string `json:"challenge_id"`
		Reference           string `json:"reference"`
		BiometricKey        string `json:"biometric_key"`
		PossessionKey       string `json:"possession_key"`
		BiometricSignature  string `json:"biometric_signature"`
		PossessionSignature string `json:"possession_signature"`
		PIN                 string `json:"pin"`
		Name                string `json:"name"`
		HardwareBacked      bool   `json:"hardware_backed"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	reg := devices.Registration{ChallengeID: body.ChallengeID, PIN: body.PIN, Name: body.Name, HardwareBacked: body.HardwareBacked}
	for _, f := range []struct {
		in  string
		out *[]byte
	}{
		{body.BiometricKey, &reg.BiometricKey}, {body.PossessionKey, &reg.PossessionKey},
		{body.BiometricSignature, &reg.BiometricSignature}, {body.PossessionSignature, &reg.PossessionSignature},
	} {
		decoded, err := base64.RawURLEncoding.DecodeString(f.in)
		if err != nil || len(decoded) == 0 {
			writeError(w, http.StatusBadRequest, "invalid_request", "claves o firmas ilegibles")
			return
		}
		*f.out = decoded
	}

	var (
		result *devices.Result
		err    error
	)
	if token := bearerToken(r); token != "" {
		principal, authErr := s.auth.Authenticate(r.Context(), token)
		if authErr != nil {
			writeError(w, http.StatusUnauthorized, "unauthenticated", "sesión inválida o expirada")
			return
		}
		result, err = s.devices.svc.AddDevice(r.Context(), principal.CustomerID, reg)
	} else {
		result, err = s.devices.svc.EnrollWithReference(r.Context(), body.Reference, reg)
	}
	if err != nil {
		s.deviceError(w, r, "vincular", err)
		return
	}
	writeJSON(w, http.StatusCreated, deviceSessionResponse{
		sessionResponse: toSessionResponse(result.Session),
		DeviceID:        result.DeviceID,
	})
}

func (s *Server) handleDeviceLoginOptions(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DeviceID string `json:"device_id"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	c, err := s.devices.svc.BeginLogin(r.Context(), body.DeviceID)
	if err != nil {
		s.deviceError(w, r, "desafío de login", err)
		return
	}
	writeJSON(w, http.StatusOK, toChallengeResponse(c))
}

func (s *Server) handleDeviceLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ChallengeID string `json:"challenge_id"`
		// Method: "biometric" (cara o huella) o "pin" (la clave de 6 dígitos).
		Method    string `json:"method"`
		Signature string `json:"signature"`
		PIN       string `json:"pin"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	signature, err := base64.RawURLEncoding.DecodeString(body.Signature)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "firma ilegible")
		return
	}

	var session *sessions.Session
	switch body.Method {
	case "biometric":
		session, err = s.devices.svc.LoginWithBiometrics(r.Context(), body.ChallengeID, signature)
	case "pin":
		session, err = s.devices.svc.LoginWithPIN(r.Context(), body.ChallengeID, signature, body.PIN)
	default:
		writeError(w, http.StatusBadRequest, "invalid_request", "method debe ser biometric o pin")
		return
	}
	if err != nil {
		s.deviceError(w, r, "login", err)
		return
	}
	writeJSON(w, http.StatusOK, deviceSessionResponse{sessionResponse: toSessionResponse(session)})
}

func (s *Server) handleRevokeDevice(w http.ResponseWriter, r *http.Request, p *Principal) {
	// Primero, que sea suyo: el teléfono de otro responde 404, como siempre.
	phones, err := s.devices.svc.List(r.Context(), p.CustomerID)
	if err != nil {
		s.deviceError(w, r, "desvincular", err)
		return
	}
	if !slices.ContainsFunc(phones, func(d devices.DeviceInfo) bool { return d.ID == r.PathValue("id") }) {
		writeError(w, http.StatusNotFound, "not_found", "no hay un teléfono vinculado con ese id")
		return
	}
	// Y no si es la única forma de entrar que le queda.
	if last, err := s.lastWayIn(r.Context(), p.CustomerID); err != nil {
		s.accessUnavailable(w, r, "formas de entrar", err)
		return
	} else if last {
		writeError(w, http.StatusConflict, "last_way_in",
			"es tu única forma de entrar: crea una passkey o vincula otro teléfono antes")
		return
	}
	err = s.devices.svc.Revoke(r.Context(), p.CustomerID, r.PathValue("id"))
	if errors.Is(err, devices.ErrRejected) {
		// Mismo 404 para "no existe" y "es de otro": no se confirma qué ids existen.
		writeError(w, http.StatusNotFound, "not_found", "no hay un teléfono vinculado con ese id")
		return
	}
	if err != nil {
		s.deviceError(w, r, "desvincular", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// deviceError traduce un fallo del dispositivo.
func (s *Server) deviceError(w http.ResponseWriter, r *http.Request, what string, err error) {
	var wrongPIN *devices.WrongPINError
	switch {
	case errors.As(err, &wrongPIN):
		writeJSON(w, http.StatusUnauthorized, map[string]any{
			"code":               "pin_incorrect",
			"message":            "clave incorrecta",
			"remaining_attempts": wrongPIN.Remaining,
		})
	case errors.Is(err, devices.ErrLocked):
		s.logger.WarnContext(r.Context(), "dispositivo bloqueado por claves incorrectas", "operación", what)
		writeError(w, http.StatusForbidden, "device_locked",
			"bloqueamos este teléfono por seguridad; entra con tu passkey o escríbenos")
	case errors.Is(err, devices.ErrRejected):
		s.logger.WarnContext(r.Context(), "dispositivo rechazado", "operación", what)
		writeError(w, http.StatusUnauthorized, "device_rejected", "este teléfono no está vinculado")
	case errors.Is(err, devices.ErrChallengeNotFound):
		writeError(w, http.StatusBadRequest, "ceremony_expired", "la solicitud venció o ya se usó; vuelve a empezar")
	case errors.Is(err, devices.ErrNotEnrollable):
		// Mismo mensaje para "no existe", "no está aprobada", "ya tiene teléfono"
		// y "venció": quien prueba referencias no aprende nada.
		writeError(w, http.StatusForbidden, "not_enrollable", "esa alta no habilita vincular un teléfono")
	case errors.Is(err, devices.ErrWeakPIN):
		writeError(w, http.StatusBadRequest, "weak_pin",
			"la clave tiene 6 dígitos y no puede ser repetida ni consecutiva")
	case errors.Is(err, devices.ErrInvalidKey):
		writeError(w, http.StatusBadRequest, "invalid_request", "claves del teléfono inválidas")
	default:
		s.logger.ErrorContext(r.Context(), "dispositivos: "+what, "error", err)
		writeError(w, http.StatusServiceUnavailable, "unavailable", "inténtalo de nuevo")
	}
}

func toChallengeResponse(c *devices.Challenge) deviceChallengeResponse {
	return deviceChallengeResponse{ChallengeID: c.ID, Payload: base64.RawURLEncoding.EncodeToString(c.Payload)}
}
