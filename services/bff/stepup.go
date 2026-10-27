package bff

import (
	"encoding/base64"
	"errors"
	"net/http"

	"github.com/aibank/aibank/services/devices"
	"github.com/aibank/aibank/services/sessions"
	"github.com/aibank/aibank/services/stepup"
)

// Confirmación reforzada de operaciones de riesgo.
//
// La primera petición de una transferencia de riesgo no mueve dinero: responde
// 428 con un desafío que lleva DENTRO el monto, el destino y la clave de la
// operación. El teléfono lo firma con la cara y la app repite la misma petición
// con la firma. Solo si lo firmado es exactamente lo que se pide ejecutar, el
// dinero se mueve.

type stepUpConfig struct {
	policy  *stepup.Service
	devices *devices.Service
}

// WithStepUp exige confirmar con el teléfono las operaciones de riesgo.
func (s *Server) WithStepUp(policy *stepup.Service, devicesSvc *devices.Service) *Server {
	s.stepUp = &stepUpConfig{policy: policy, devices: devicesSvc}
	return s
}

type transferConfirmation struct {
	ChallengeID string `json:"challenge_id"`
	// Method: "biometric" (la cara) o "pin" (la clave de 6 dígitos).
	Method    string `json:"method"`
	Signature string `json:"signature"`
	PIN       string `json:"pin,omitempty"`
}

type confirmationRequired struct {
	Code        string `json:"code"`
	Message     string `json:"message"`
	ChallengeID string `json:"challenge_id"`
	// Payload son los bytes exactos que el teléfono firma, en base64url.
	Payload string `json:"payload"`
	Reason  string `json:"reason"`
	// Summary es lo que se le muestra a la persona al pedirle la cara.
	Summary confirmationSummary `json:"summary"`
}

type confirmationSummary struct {
	AmountMicros int64  `json:"amount_micros"`
	Currency     string `json:"currency"`
	ToName       string `json:"to_name"`
}

// stepUpTransfer decide si la transferencia necesita confirmación y, si la
// trae, la verifica. Devuelve si quedó confirmada, y `ok=false` cuando ya
// respondió y la transferencia no debe seguir.
func (s *Server) stepUpTransfer(w http.ResponseWriter, r *http.Request, p *Principal, t stepup.Transfer, toName string, c *transferConfirmation) (confirmed, ok bool) {
	// Sin política, o con el sustituto de desarrollo del sandbox (que no tiene
	// teléfono que firme), no hay confirmación que pedir.
	if s.stepUp == nil || p.Factor == "" {
		return false, true
	}
	ctx := r.Context()
	reason, err := s.stepUp.policy.Required(ctx, t)
	if err != nil {
		s.logger.ErrorContext(ctx, "política de confirmación", "error", err)
		writeError(w, http.StatusServiceUnavailable, "unavailable", "inténtalo de nuevo")
		return false, false
	}
	if reason == stepup.ReasonNone {
		return false, true
	}

	isDevice := p.Factor == string(sessions.FactorDeviceBiometric) || p.Factor == string(sessions.FactorDevicePIN)
	if !isDevice {
		// Entró con una passkey en un teléfono sin vincular: no hay llave del
		// teléfono con la que firmar la operación.
		writeError(w, http.StatusForbidden, "confirmation_unavailable",
			"confirma esta operación desde tu teléfono vinculado")
		return false, false
	}

	if c == nil {
		challenge, err := s.stepUp.devices.BeginConfirmation(ctx, p.CustomerID, p.DeviceID, t.Operation())
		if err != nil {
			s.deviceError(w, r, "desafío de confirmación", err)
			return false, false
		}
		writeJSON(w, http.StatusPreconditionRequired, confirmationRequired{
			Code:        "confirmation_required",
			Message:     "confirma la operación con tu teléfono",
			ChallengeID: challenge.ID,
			Payload:     base64.RawURLEncoding.EncodeToString(challenge.Payload),
			Reason:      string(reason),
			Summary:     confirmationSummary{AmountMicros: t.AmountMicros, Currency: t.Currency, ToName: toName},
		})
		return false, false
	}

	signature, err := base64.RawURLEncoding.DecodeString(c.Signature)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "firma ilegible")
		return false, false
	}
	err = s.stepUp.devices.Confirm(ctx, p.CustomerID, p.DeviceID, c.ChallengeID, t.Operation(),
		devices.Method(c.Method), signature, c.PIN)
	if errors.Is(err, devices.ErrRejected) {
		// Firma que no verifica, de otro teléfono, o de OTRA operación: el mismo
		// rechazo. Para afuera no se distingue; para adentro, va al log.
		s.logger.WarnContext(ctx, "confirmación rechazada", "cliente", p.CustomerID)
		writeError(w, http.StatusForbidden, "confirmation_rejected", "no pudimos confirmar la operación")
		return false, false
	}
	if err != nil {
		s.deviceError(w, r, "confirmar", err)
		return false, false
	}
	return true, true
}
