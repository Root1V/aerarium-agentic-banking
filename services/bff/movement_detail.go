package bff

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/aibank/aibank/clients/go/coreclient"
	corev1 "github.com/aibank/aibank/clients/go/corev1"
	"github.com/aibank/aibank/services/control"
)

// El detalle de un movimiento: lo que el historial no alcanza a decir.
//
// Además del estado de un pago (¿me lo cobraron o volvió?), dos cosas que el
// banco sabe y otros no muestran:
//
//   - Cómo se autorizó: si lo confirmó la persona con la cara o la clave y
//     desde qué teléfono, si no pidió confirmación y por qué, o qué agente lo
//     pagó con su firma y dentro de qué permiso. Responde «¿esto lo hice yo?».
//   - La tarea completa del agente: un micropago suelto no dice nada; «parte de
//     la tarea vuelos a Cusco: 3 pagos, 1,50 USD», sí.

type paymentStatusJSON struct {
	// Status: `held` (retenido, todavía puede cobrarse o volver), `captured`,
	// `partially_refunded`, `refunded`, `released` (no se cobró y volvió) o
	// `expired` (venció sin cobrarse y vuelve).
	Status    string    `json:"status"`
	Amount    moneyJSON `json:"amount"`
	Refunded  moneyJSON `json:"refunded"`
	ExpiresAt time.Time `json:"expires_at"`
}

type authorizationJSON struct {
	// ActorKind: `holder` o `agent`.
	ActorKind string `json:"actor_kind"`
	// Outcome: `allow` (no pidió confirmación) o `confirmed`.
	Outcome string `json:"outcome"`
	// Reason: por qué pidió confirmación (`new_payee`, `amount`, `high_impact`)
	// o por qué no (`known_payee`).
	Reason string `json:"reason,omitempty"`
	// Method: `biometric` o `pin`, si se confirmó.
	Method string `json:"method,omitempty"`
	// Device: el teléfono con el que se hizo. Vacío si ya se desvinculó.
	Device string `json:"device,omitempty"`
	// Agent y Platform: quién pagó, si fue un agente.
	Agent    string `json:"agent,omitempty"`
	Platform string `json:"platform,omitempty"`
	// Signed: el agente firmó el pago con su clave.
	Signed bool `json:"signed,omitempty"`
	// El permiso con el que pagó.
	MandateMaxPerOperation *moneyJSON `json:"mandate_max_per_operation,omitempty"`
	MandateExpiresAt       *time.Time `json:"mandate_expires_at,omitempty"`
}

type taskPaymentJSON struct {
	Description string    `json:"description"`
	Amount      moneyJSON `json:"amount"`
	At          time.Time `json:"at"`
	// This: es el movimiento que se está mirando.
	This bool `json:"this,omitempty"`
}

type taskJSON struct {
	Reference string            `json:"reference"`
	Count     int               `json:"count"`
	Total     moneyJSON         `json:"total"`
	Payments  []taskPaymentJSON `json:"payments"`
}

type movementDetailJSON struct {
	Payment       *paymentStatusJSON `json:"payment,omitempty"`
	Authorization *authorizationJSON `json:"authorization,omitempty"`
	Task          *taskJSON          `json:"task,omitempty"`
}

// taskLimit: una tarea con más pagos se resume en los primeros.
const taskLimit = 20

func (s *Server) handleMovementDetail(w http.ResponseWriter, r *http.Request, p *Principal) {
	ctx := r.Context()
	accountID, txID := r.PathValue("accountID"), r.PathValue("transactionID")
	if _, err := s.authorizeAccount(ctx, p, accountID); err != nil {
		s.writeCoreError(w, r, err)
		return
	}
	var out movementDetailJSON

	// ¿Es el pago de una plataforma? Entonces tiene autorización en el core.
	holds, err := s.core.LookupHolds(ctx, []string{txID})
	if err != nil {
		s.writeCoreError(w, r, err)
		return
	}
	var auth *coreclient.Authorization
	if hold, ok := holds[txID]; ok {
		auth, err = s.core.GetAuthorization(ctx, hold.AuthorizationID)
		if err != nil {
			s.writeCoreError(w, r, err)
			return
		}
		if auth.PayerAccountID != accountID {
			// La retención no es de esta cuenta: para quien pregunta, no existe.
			auth = nil
		}
	}
	if auth != nil {
		out.Payment = &paymentStatusJSON{Status: paymentStatusName(auth),
			Amount:    moneyJSON{AmountMicros: auth.AmountMicros, Currency: auth.Currency},
			Refunded:  moneyJSON{AmountMicros: auth.RefundedMicros, Currency: auth.Currency},
			ExpiresAt: auth.ExpiresAt}
		out.Task = s.taskOf(ctx, accountID, auth)
	}

	if s.control != nil {
		execution, err := s.control.control.ExecutionOf(ctx, p.CustomerID, txID)
		if err != nil {
			s.logger.WarnContext(ctx, "cómo se autorizó el movimiento", "error", err)
		} else if execution != nil {
			out.Authorization = s.authorizationJSON(ctx, p.CustomerID, execution)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// authorizationJSON pone nombres a la evidencia: el teléfono, el agente, el permiso.
func (s *Server) authorizationJSON(ctx context.Context, customerID string, e *control.Execution) *authorizationJSON {
	out := &authorizationJSON{ActorKind: string(e.Actor.Kind), Outcome: e.Outcome, Reason: string(e.Reason)}
	evidence := map[string]string{}
	for _, part := range strings.Split(e.Evidence, ";") {
		if k, v, ok := strings.Cut(part, ":"); ok {
			evidence[k] = v
		}
	}
	switch e.Actor.Kind {
	case control.ActorHolder:
		out.Method = evidence["method"]
		if s.devices != nil {
			if phones, err := s.devices.svc.List(ctx, customerID); err == nil {
				for _, d := range phones {
					if d.ID == evidence["device"] {
						out.Device = d.Name
					}
				}
			}
		}
	case control.ActorAgent:
		out.Signed = evidence["key"] != ""
		if s.agents != nil {
			if names, err := s.agents.Names(ctx, []string{e.Actor.ID}); err == nil {
				if n, ok := names[e.Actor.ID]; ok {
					out.Agent, out.Platform = n.Name, s.merchantName(ctx, n.ClientID)
				}
			}
		}
		if id := evidence["mandate"]; id != "" {
			if m, err := s.core.GetMandate(ctx, id); err == nil {
				if m.MaxPerOperation != nil {
					out.MandateMaxPerOperation = &moneyJSON{AmountMicros: *m.MaxPerOperation, Currency: m.Currency}
				}
				expires := m.ExpiresAt
				out.MandateExpiresAt = &expires
				if out.Platform == "" {
					out.Platform = s.merchantName(ctx, m.Grantee)
				}
			}
		}
	}
	return out
}

// taskOf reúne los pagos de la misma tarea del agente, desde esta cuenta.
func (s *Server) taskOf(ctx context.Context, accountID string, auth *coreclient.Authorization) *taskJSON {
	if s.agents == nil {
		return nil
	}
	intents, err := s.agents.IntentsFor(ctx, []string{auth.ID})
	if err != nil {
		return nil
	}
	intent, ok := intents[auth.ID]
	if !ok {
		return nil
	}
	payments, err := s.agents.TaskPayments(ctx, intent.AgentID, intent.Reference, taskLimit)
	if err != nil || len(payments) < 2 {
		// Un pago solo no es una tarea que valga la pena mostrar.
		return nil
	}
	task := &taskJSON{Reference: intent.Reference, Total: moneyJSON{Currency: auth.Currency}}
	for _, p := range payments {
		a, err := s.core.GetAuthorization(ctx, p.AuthorizationID)
		if err != nil || a.PayerAccountID != accountID {
			continue
		}
		// Lo que se devolvió no se gastó.
		spent := a.AmountMicros - a.RefundedMicros
		if a.Status == corev1.AuthorizationStatus_AUTHORIZATION_STATUS_RELEASED ||
			a.Status == corev1.AuthorizationStatus_AUTHORIZATION_STATUS_EXPIRED {
			spent = 0
		}
		task.Total.AmountMicros += spent
		task.Count++
		task.Payments = append(task.Payments, taskPaymentJSON{Description: p.Description,
			Amount: moneyJSON{AmountMicros: spent, Currency: a.Currency}, At: p.CreatedAt, This: a.ID == auth.ID})
	}
	if task.Count < 2 {
		return nil
	}
	return task
}

func paymentStatusName(a *coreclient.Authorization) string {
	switch a.Status {
	case corev1.AuthorizationStatus_AUTHORIZATION_STATUS_AUTHORIZED:
		if time.Now().After(a.ExpiresAt) {
			return "expired"
		}
		return "held"
	case corev1.AuthorizationStatus_AUTHORIZATION_STATUS_CAPTURED:
		return "captured"
	case corev1.AuthorizationStatus_AUTHORIZATION_STATUS_PARTIALLY_REFUNDED:
		return "partially_refunded"
	case corev1.AuthorizationStatus_AUTHORIZATION_STATUS_REFUNDED:
		return "refunded"
	case corev1.AuthorizationStatus_AUTHORIZATION_STATUS_RELEASED:
		return "released"
	case corev1.AuthorizationStatus_AUTHORIZATION_STATUS_EXPIRED:
		return "expired"
	default:
		return "unknown"
	}
}
