package bff

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"

	"github.com/aibank/aibank/services/control"
	"github.com/aibank/aibank/services/devices"
	"github.com/aibank/aibank/services/oauth"
	"github.com/aibank/aibank/services/sessions"
	"github.com/aibank/aibank/services/stepup"
)

// La capa de control en el canal del titular, y la confirmación reforzada.
//
// Toda operación que mueve dinero o da poder sobre él pasa por control.Decide.
// Si la decisión es confirmar, la primera petición no hace nada: responde 428
// con un desafío que lleva DENTRO la operación entera. El teléfono lo firma con
// la cara y la app repite la misma petición con la firma. Solo si lo firmado es
// exactamente lo que se pide ejecutar, la operación sigue.

type controlConfig struct {
	control *control.Service
	// payees recuerda a quién se le envió: los envíos chicos a conocidos no se
	// confirman, y es la lista de recientes.
	payees  *stepup.Service
	devices *devices.Service
}

// WithControl hace pasar las operaciones del titular por la capa de control, y
// confirmar con el teléfono las que ella diga.
func (s *Server) WithControl(ctrl *control.Service, payees *stepup.Service, devicesSvc *devices.Service) *Server {
	s.control = &controlConfig{control: ctrl, payees: payees, devices: devicesSvc}
	return s
}

// operationConfirmation es la firma que trae la segunda petición.
type operationConfirmation struct {
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
	// Kind dice qué se confirma: "transfer" o "grant_permission".
	Kind         string `json:"kind"`
	AmountMicros int64  `json:"amount_micros"`
	Currency     string `json:"currency"`
	// ToName: a quién se le envía, o a qué plataforma se le da el permiso.
	ToName string `json:"to_name"`
	// AgentName: el agente de esa plataforma que podrá usar el permiso.
	AgentName string `json:"agent_name,omitempty"`
	// Los topes del permiso que se da. Ausentes: sin tope.
	MaxPerOperation *int64 `json:"max_per_operation,omitempty"`
	MaxTotal        *int64 `json:"max_total,omitempty"`
	ExpiresInDays   int    `json:"expires_in_days,omitempty"`
}

// holderOperation es una operación del titular por decidir.
type holderOperation struct {
	action       control.Action
	amountMicros int64
	counterparty string
	// canonical es lo que firma el teléfono.
	canonical []byte
	summary   confirmationSummary
}

// authorizeOperation pasa la operación por la capa de control y, si hay que
// confirmarla y trae la firma, la verifica. Devuelve `false` cuando ya respondió
// y la operación no debe seguir.
func (s *Server) authorizeOperation(w http.ResponseWriter, r *http.Request, p *Principal, op holderOperation, c *operationConfirmation) bool {
	// Sin capa de control, o con el sustituto de desarrollo del sandbox (que no
	// tiene teléfono que firme), no hay nada que decidir.
	if s.control == nil || p.Factor == "" {
		return true
	}
	ctx := r.Context()
	req := control.Request{
		CustomerID:   p.CustomerID,
		Actor:        control.Actor{Kind: control.ActorHolder, ID: p.DeviceID},
		Action:       op.action,
		AmountMicros: op.amountMicros,
		Counterparty: op.counterparty,
		Operation:    op.canonical,
	}

	// Cada petición se decide, también la segunda, la que trae la firma: si
	// entretanto la operación dejó de pedir confirmación, sigue sin ella; si se
	// volvió inadmisible, la firma no la salva.
	decision, err := s.control.control.Decide(ctx, req)
	if err != nil {
		s.logger.ErrorContext(ctx, "capa de control", "error", err)
		writeError(w, http.StatusServiceUnavailable, "unavailable", "inténtalo de nuevo")
		return false
	}
	switch decision.Outcome {
	case control.Allow:
		return true
	case control.Deny:
		writeError(w, http.StatusForbidden, "not_allowed", "esta operación no está permitida")
		return false
	}

	isDevice := p.Factor == string(sessions.FactorDeviceBiometric) || p.Factor == string(sessions.FactorDevicePIN)
	if !isDevice {
		// Entró con una passkey en un teléfono sin vincular: no hay llave del
		// teléfono con la que firmar la operación.
		writeError(w, http.StatusForbidden, "confirmation_unavailable",
			"confirma esta operación desde tu teléfono vinculado")
		return false
	}
	if c != nil {
		return s.verifyConfirmation(w, r, p, req, c)
	}
	challenge, err := s.control.devices.BeginConfirmation(ctx, p.CustomerID, p.DeviceID, op.canonical)
	if err != nil {
		s.deviceError(w, r, "desafío de confirmación", err)
		return false
	}
	writeJSON(w, http.StatusPreconditionRequired, confirmationRequired{
		Code:        "confirmation_required",
		Message:     "confirma la operación con tu teléfono",
		ChallengeID: challenge.ID,
		Payload:     base64.RawURLEncoding.EncodeToString(challenge.Payload),
		Reason:      string(decision.Reason),
		Summary:     op.summary,
	})
	return false
}

func (s *Server) verifyConfirmation(w http.ResponseWriter, r *http.Request, p *Principal, req control.Request, c *operationConfirmation) bool {
	ctx := r.Context()
	signature, err := base64.RawURLEncoding.DecodeString(c.Signature)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "firma ilegible")
		return false
	}
	err = s.control.devices.Confirm(ctx, p.CustomerID, p.DeviceID, c.ChallengeID, req.Operation,
		devices.Method(c.Method), signature, c.PIN)
	evidence := "device:" + p.DeviceID + ";method:" + c.Method
	if errors.Is(err, devices.ErrRejected) {
		// Firma que no verifica, de otro teléfono, o de OTRA operación: el mismo
		// rechazo. Para afuera no se distingue; para adentro, va al log.
		s.logger.WarnContext(ctx, "confirmación rechazada", "cliente", p.CustomerID)
		if err := s.control.control.Confirmed(ctx, req, false, evidence); err != nil {
			s.logger.ErrorContext(ctx, "registrar confirmación rechazada", "error", err)
		}
		writeError(w, http.StatusForbidden, "confirmation_rejected", "no pudimos confirmar la operación")
		return false
	}
	if err != nil {
		s.deviceError(w, r, "confirmar", err)
		return false
	}
	// Sin constancia de la confirmación, la operación no sigue: es la prueba de
	// que la persona la aprobó.
	if err := s.control.control.Confirmed(ctx, req, true, evidence); err != nil {
		s.logger.ErrorContext(ctx, "registrar confirmación", "error", err)
		writeError(w, http.StatusServiceUnavailable, "unavailable", "inténtalo de nuevo")
		return false
	}
	return true
}

// transferOperation describe una transferencia para la capa de control.
func transferOperation(t stepup.Transfer, toName string) holderOperation {
	return holderOperation{
		action:       control.ActionTransfer,
		amountMicros: t.AmountMicros,
		counterparty: t.ToAccountID,
		canonical:    t.Operation(),
		summary: confirmationSummary{Kind: string(control.ActionTransfer),
			AmountMicros: t.AmountMicros, Currency: t.Currency, ToName: toName},
	}
}

// grantOperation describe darle a una plataforma permiso para pagar. Lo firmado
// lleva la cuenta, los topes y la vigencia: la firma de un permiso de 10 no
// sirve para conceder uno sin límite.
func grantOperation(request *oauth.ConsentRequest, body approveConsentBody, grantedTo string) holderOperation {
	limit := func(v *int64) string {
		if v == nil {
			return "none"
		}
		return strconv.FormatInt(*v, 10)
	}
	return holderOperation{
		action:       control.ActionGrantPermission,
		counterparty: request.ClientID,
		canonical: []byte("grant_permission" +
			"\nconsent=" + request.ID +
			"\naccount=" + body.AccountID +
			"\ngrantee=" + request.ClientID +
			"\nagent=" + request.AgentID +
			"\nmax_per_operation=" + limit(body.MaxPerOperation) +
			"\nmax_total=" + limit(body.MaxTotal) +
			"\ncurrency=" + request.Currency +
			"\nexpires_in_days=" + strconv.Itoa(body.ExpiresInDays)),
		summary: confirmationSummary{Kind: string(control.ActionGrantPermission),
			Currency: request.Currency, ToName: grantedTo, AgentName: request.AgentName,
			MaxPerOperation: body.MaxPerOperation, MaxTotal: body.MaxTotal, ExpiresInDays: body.ExpiresInDays},
	}
}

// handleRecentPayees devuelve a quién le envió dinero el titular, del más
// reciente al más antiguo. Es lo que permite enviar sin escribir una cuenta.
func (s *Server) handleRecentPayees(w http.ResponseWriter, r *http.Request, p *Principal) {
	payees := []accountJSON{}
	if s.control != nil {
		ids, err := s.control.payees.Recent(r.Context(), p.CustomerID, recentPayeesLimit)
		if err != nil {
			s.logger.ErrorContext(r.Context(), "destinatarios recientes", "error", err)
			writeError(w, http.StatusServiceUnavailable, "unavailable", "inténtalo de nuevo")
			return
		}
		for _, id := range ids {
			account, err := s.core.GetAccountByID(r.Context(), id)
			if err != nil {
				// Una cuenta que ya no existe o no responde no tumba la lista.
				continue
			}
			payees = append(payees, accountJSON{ID: account.Id, Name: account.Name, Currency: account.Currency})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"payees": payees})
}

const recentPayeesLimit = 10
