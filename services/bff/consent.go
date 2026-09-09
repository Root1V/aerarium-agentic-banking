package bff

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/aibank/aibank/clients/go/coreclient"
	corev1 "github.com/aibank/aibank/clients/go/corev1"
	"github.com/aibank/aibank/services/oauth"
)

// El lado del TITULAR del modelo B: ver qué le está pidiendo una plataforma,
// concederlo con sus propios topes, y retirarlo cuando quiera.
//
// Vive en el BFF y no en la API de socio a propósito. Estas operaciones las hace
// una persona autenticada con su dispositivo, no una máquina con una credencial;
// mezclarlas en el mismo servicio haría que un fallo en la autorización de un
// canal alcanzara al otro.
//
// Que la decisión ocurra AQUÍ es lo que hace que el mandato pruebe algo: la
// persona se autentica contra el banco, en la app del banco, y ve en pantalla lo
// que aprueba. Si la pantalla la pusiera la plataforma, lo único que tendríamos
// sería su palabra de que la mostró.

type consentPromptJSON struct {
	ConsentRequestID string `json:"consent_request_id"`
	// Quién pide. Se muestra tal cual en la pantalla.
	RequestedBy string `json:"requested_by"`
	Purpose     string `json:"purpose"`
	Currency    string `json:"currency"`
	// Lo que se pide. El titular puede conceder menos.
	MaxPerOperation *int64    `json:"max_per_operation,omitempty"`
	MaxTotal        *int64    `json:"max_total,omitempty"`
	Status          string    `json:"status"`
	ExpiresAt       time.Time `json:"expires_at"`
}

// handleConsentPrompt devuelve lo que la app tiene que mostrarle al titular.
func (s *Server) handleConsentPrompt(w http.ResponseWriter, r *http.Request, _ *Principal) {
	request, err := s.consents.FindConsentRequest(r.Context(), r.PathValue("handoff"))
	if errors.Is(err, oauth.ErrConsentNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "esa solicitud no existe o ya venció")
		return
	}
	if err != nil {
		s.logger.ErrorContext(r.Context(), "buscar solicitud de consentimiento", "error", err)
		writeError(w, http.StatusServiceUnavailable, "unavailable", "no se pudo consultar la solicitud")
		return
	}

	client, err := s.consents.FindClient(r.Context(), request.ClientID)
	if err != nil {
		s.logger.ErrorContext(r.Context(), "buscar integración", "error", err)
		writeError(w, http.StatusServiceUnavailable, "unavailable", "no se pudo consultar la solicitud")
		return
	}

	writeJSON(w, http.StatusOK, consentPromptJSON{
		ConsentRequestID: request.ID,
		// El nombre registrado de la integración, no uno que ella mande en la
		// petición: si la plataforma pudiera elegir cómo se presenta en la
		// pantalla del banco, podría hacerse pasar por otra.
		RequestedBy:     client.Name,
		Purpose:         request.Purpose,
		Currency:        request.Currency,
		MaxPerOperation: request.RequestedMaxPerOperation,
		MaxTotal:        request.RequestedMaxTotal,
		Status:          request.ObservedStatus(),
		ExpiresAt:       request.ExpiresAt,
	})
}

type approveConsentBody struct {
	// Cuenta desde la que el titular autoriza pagar. Debe ser suya.
	AccountID string `json:"account_id"`
	// Topes que concede. Pueden ser menores a los pedidos, nunca mayores.
	MaxPerOperation *int64 `json:"max_per_operation,omitempty"`
	MaxTotal        *int64 `json:"max_total,omitempty"`
	// Días de vigencia del permiso.
	ExpiresInDays int `json:"expires_in_days"`
}

type mandateJSON struct {
	MandateID       string     `json:"mandate_id"`
	AccountID       string     `json:"account_id"`
	GrantedTo       string     `json:"granted_to"`
	Currency        string     `json:"currency"`
	Status          string     `json:"status"`
	MaxPerOperation *int64     `json:"max_per_operation,omitempty"`
	MaxTotal        *int64     `json:"max_total,omitempty"`
	Consumed        int64      `json:"consumed"`
	Remaining       *int64     `json:"remaining,omitempty"`
	ExpiresAt       time.Time  `json:"expires_at"`
	RevokedAt       *time.Time `json:"revoked_at,omitempty"`
}

// handleApproveConsent otorga el permiso.
func (s *Server) handleApproveConsent(w http.ResponseWriter, r *http.Request, principal *Principal) {
	var body approveConsentBody
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.ExpiresInDays <= 0 || body.ExpiresInDays > 365 {
		writeError(w, http.StatusBadRequest, "invalid_request",
			"la vigencia debe estar entre 1 y 365 días")
		return
	}

	request, err := s.consents.FindConsentRequest(r.Context(), r.PathValue("handoff"))
	if errors.Is(err, oauth.ErrConsentNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "esa solicitud no existe")
		return
	}
	if err != nil {
		s.logger.ErrorContext(r.Context(), "buscar solicitud", "error", err)
		writeError(w, http.StatusServiceUnavailable, "unavailable", "no se pudo consultar la solicitud")
		return
	}

	// El titular puede conceder MENOS de lo que se le pidió, nunca más.
	if err := oauth.CheckGrantable(request, body.MaxPerOperation, body.MaxTotal); err != nil {
		switch {
		case errors.Is(err, oauth.ErrConsentExpired):
			writeError(w, http.StatusConflict, "expired", "la solicitud venció; pide una nueva")
		case errors.Is(err, oauth.ErrConsentResolved):
			writeError(w, http.StatusConflict, "already_resolved", "esa solicitud ya fue respondida")
		default:
			writeError(w, http.StatusUnprocessableEntity, "limit_above_requested",
				"no puedes conceder más de lo que se te pidió")
		}
		return
	}

	// La cuenta tiene que ser del titular autenticado. Es la barrera que impide
	// otorgar permiso sobre el dinero de otra persona; el core la vuelve a
	// comprobar, y esa redundancia es deliberada.
	account, err := s.core.GetAccountByID(r.Context(), body.AccountID)
	if err != nil || account.OwnerId != principal.CustomerID {
		writeError(w, http.StatusNotFound, "not_found", "esa cuenta no existe")
		return
	}

	mandate, err := s.core.GrantMandate(r.Context(), body.AccountID, request.ClientID,
		principal.CustomerID,
		// La evidencia del consentimiento: qué solicitud y qué dispositivo la
		// aprobó. Es lo que se presenta si alguien dice que nunca autorizó nada.
		"consent:"+request.ID+";device:"+principal.DeviceID,
		body.MaxPerOperation, body.MaxTotal,
		time.Now().AddDate(0, 0, body.ExpiresInDays))
	if err != nil {
		s.logger.ErrorContext(r.Context(), "otorgar mandato", "error", err)
		if errors.Is(err, coreclient.ErrInvalid) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_request",
				"no se puede otorgar el permiso sobre esa cuenta")
			return
		}
		writeError(w, http.StatusServiceUnavailable, "unavailable", "no se pudo otorgar el permiso")
		return
	}

	if err := s.consents.ApproveConsentRequest(r.Context(), request.ID, mandate.ID, body.AccountID); err != nil {
		// El mandato ya existe. Se revoca para no dejar un permiso vivo que la
		// plataforma nunca va a poder encontrar y el titular no espera tener.
		if _, revokeErr := s.core.RevokeMandate(r.Context(), mandate.ID, "rollback"); revokeErr != nil {
			s.logger.ErrorContext(r.Context(), "no se pudo revertir el mandato huérfano",
				"mandate_id", mandate.ID, "error", revokeErr)
		}
		writeError(w, http.StatusConflict, "already_resolved", "esa solicitud ya fue respondida")
		return
	}

	writeJSON(w, http.StatusCreated, toMandateJSON(mandate))
}

// handleRejectConsent rechaza la solicitud sin otorgar nada.
func (s *Server) handleRejectConsent(w http.ResponseWriter, r *http.Request, _ *Principal) {
	request, err := s.consents.FindConsentRequest(r.Context(), r.PathValue("handoff"))
	if errors.Is(err, oauth.ErrConsentNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "esa solicitud no existe")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "no se pudo consultar la solicitud")
		return
	}

	if err := s.consents.RejectConsentRequest(r.Context(), request.ID); err != nil {
		writeError(w, http.StatusConflict, "already_resolved", "esa solicitud ya fue respondida")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleListMandates muestra los permisos que el titular tiene otorgados.
//
// Sin esta pantalla, revocar sería imposible en la práctica: no se puede retirar
// un permiso que no se puede ver.
func (s *Server) handleListMandates(w http.ResponseWriter, r *http.Request, principal *Principal) {
	mandates, err := s.core.ListMandates(r.Context(), principal.CustomerID)
	if err != nil {
		s.logger.ErrorContext(r.Context(), "listar mandatos", "error", err)
		writeError(w, http.StatusServiceUnavailable, "unavailable", "no se pudieron consultar los permisos")
		return
	}

	out := make([]mandateJSON, 0, len(mandates))
	for _, m := range mandates {
		out = append(out, toMandateJSON(m))
	}
	writeJSON(w, http.StatusOK, map[string]any{"mandates": out})
}

// handleRevokeMandate retira un permiso.
func (s *Server) handleRevokeMandate(w http.ResponseWriter, r *http.Request, principal *Principal) {
	mandate, err := s.core.GetMandate(r.Context(), r.PathValue("mandateID"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "ese permiso no existe")
		return
	}
	// Un permiso de otra persona no existe para esta.
	if mandate.GrantedBy != principal.CustomerID {
		writeError(w, http.StatusNotFound, "not_found", "ese permiso no existe")
		return
	}

	revoked, err := s.core.RevokeMandate(r.Context(), mandate.ID, "titular")
	if err != nil {
		s.logger.ErrorContext(r.Context(), "revocar mandato", "error", err)
		writeError(w, http.StatusServiceUnavailable, "unavailable", "no se pudo retirar el permiso")
		return
	}
	writeJSON(w, http.StatusOK, toMandateJSON(revoked))
}

// decodeJSON lee el cuerpo con un tope de tamaño y rechaza campos desconocidos.
//
// Un campo mal escrito casi siempre es una errata, y aceptarla en silencio deja
// al cliente creyendo que mandó un tope que el servidor nunca vio.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "cuerpo inválido: "+err.Error())
		return false
	}
	return true
}

func toMandateJSON(m *coreclient.Mandate) mandateJSON {
	return mandateJSON{
		MandateID:       m.ID,
		AccountID:       m.AccountID,
		GrantedTo:       m.Grantee,
		Currency:        m.Currency,
		Status:          mandateStatusName(m.Status),
		MaxPerOperation: m.MaxPerOperation,
		MaxTotal:        m.MaxTotal,
		Consumed:        m.ConsumedMicros,
		Remaining:       m.RemainingMicros(),
		ExpiresAt:       m.ExpiresAt,
		RevokedAt:       m.RevokedAt,
	}
}

func mandateStatusName(status corev1.MandateStatus) string {
	switch status {
	case corev1.MandateStatus_MANDATE_STATUS_ACTIVE:
		return "active"
	case corev1.MandateStatus_MANDATE_STATUS_REVOKED:
		return "revoked"
	case corev1.MandateStatus_MANDATE_STATUS_EXPIRED:
		return "expired"
	default:
		return "unknown"
	}
}
