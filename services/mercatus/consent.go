package mercatus

import (
	"errors"
	"net/http"
	"time"

	"github.com/aibank/aibank/services/oauth"
)

// Endpoints del **modelo B**: la plataforma pide permiso, el titular lo otorga
// desde su app, y a partir de ahí la plataforma puede iniciar pagos sobre una
// cuenta que no es suya.
//
// Lo que distingue este flujo del modelo ómnibus es dónde ocurre la decisión.
// Aquí la plataforma no elige la cuenta ni el tope: los pide. Quien elige es el
// titular, autenticado contra el banco, en una pantalla del banco.

type consentRequestBody struct {
	// Cuánto necesita la plataforma. El titular puede conceder menos.
	MaxPerOperation *int64 `json:"max_per_operation,omitempty"`
	MaxTotal        *int64 `json:"max_total,omitempty"`
	Currency        string `json:"currency"`
	// Para qué, en palabras que una persona entienda. Se muestra en la pantalla
	// de consentimiento, así que un texto vago se convierte en una decisión que
	// el titular no puede tomar bien.
	Purpose string `json:"purpose"`
}

type consentRequestResponse struct {
	ConsentRequestID string `json:"consent_request_id"`
	// HandoffCode es lo que el titular abre en su app. Va en un enlace o un QR.
	HandoffCode string    `json:"handoff_code"`
	Status      string    `json:"status"`
	ExpiresAt   time.Time `json:"expires_at"`
	// Presentes solo cuando el titular ya aprobó.
	MandateID string `json:"mandate_id,omitempty"`
	AccountID string `json:"account_id,omitempty"`
}

func (s *Server) handleCreateConsentRequest(w http.ResponseWriter, r *http.Request, claims *oauth.Claims) {
	var req consentRequestBody
	if !decodeBody(w, r, &req) {
		return
	}
	if len(req.Currency) != 3 {
		writeError(w, http.StatusBadRequest, CodeMalformedRequest, "currency debe ser ISO-4217 de 3 letras")
		return
	}
	if req.Purpose == "" {
		// Sin propósito, la pantalla de consentimiento le pide a una persona que
		// apruebe algo que no puede evaluar.
		writeError(w, http.StatusBadRequest, CodeMalformedRequest,
			"purpose es obligatorio: es lo que el titular lee antes de decidir")
		return
	}
	for name, value := range map[string]*int64{
		"max_per_operation": req.MaxPerOperation,
		"max_total":         req.MaxTotal,
	} {
		if value != nil && *value <= 0 {
			writeError(w, http.StatusBadRequest, CodeMalformedRequest, name+" debe ser mayor que cero")
			return
		}
	}

	created, err := s.store.NewConsentRequest(r.Context(), oauth.ConsentRequest{
		ClientID:                 claims.Subject,
		RequestedMaxPerOperation: req.MaxPerOperation,
		RequestedMaxTotal:        req.MaxTotal,
		Currency:                 req.Currency,
		Purpose:                  req.Purpose,
	})
	if err != nil {
		s.log.ErrorContext(r.Context(), "crear solicitud de consentimiento", "error", err)
		writeError(w, http.StatusInternalServerError, CodeServerError, "error interno")
		return
	}

	writeJSON(w, http.StatusCreated, consentRequestResponse{
		ConsentRequestID: created.ID,
		HandoffCode:      created.HandoffCode,
		Status:           statusLabel(created.ObservedStatus()),
		ExpiresAt:        created.ExpiresAt,
	})
}

// handleGetConsentRequest la consulta la plataforma para saber si ya se aprobó.
//
// Se consulta, no se notifica: un webhook exigiría que la plataforma exponga un
// endpoint y que el banco firme y reintente. Para una decisión que tarda lo que
// tarda una persona en mirar su teléfono, consultar es más simple para las dos
// partes y no deja un endpoint público más que defender.
func (s *Server) handleGetConsentRequest(w http.ResponseWriter, r *http.Request, claims *oauth.Claims) {
	request, err := s.store.FindConsentRequest(r.Context(), r.PathValue("id"))
	if errors.Is(err, oauth.ErrConsentNotFound) {
		writeError(w, http.StatusNotFound, CodeConsentNotFound, "la solicitud no existe")
		return
	}
	if err != nil {
		s.log.ErrorContext(r.Context(), "buscar solicitud", "error", err)
		writeError(w, http.StatusInternalServerError, CodeServerError, "error interno")
		return
	}

	// Una solicitud de otra integración responde igual que una inexistente: si se
	// distinguieran, un socio podría descubrir qué solicitudes hay en curso.
	if request.ClientID != claims.Subject {
		writeError(w, http.StatusNotFound, CodeConsentNotFound, "la solicitud no existe")
		return
	}

	response := consentRequestResponse{
		ConsentRequestID: request.ID,
		HandoffCode:      request.HandoffCode,
		Status:           statusLabel(request.ObservedStatus()),
		ExpiresAt:        request.ExpiresAt,
	}
	if request.ObservedStatus() == oauth.ConsentApproved {
		response.MandateID = request.MandateID
		response.AccountID = encodeID(accountPrefix, request.AccountID)
	}
	writeJSON(w, http.StatusOK, response)
}

// handleGetMandate deja que la plataforma consulte el permiso vigente: cuánto
// queda, hasta cuándo, si sigue vivo.
func (s *Server) handleGetMandate(w http.ResponseWriter, r *http.Request, claims *oauth.Claims) {
	mandate, err := s.core.GetMandate(r.Context(), r.PathValue("id"))
	if err != nil {
		mandateError(w, err)
		return
	}
	// Un mandato de otra integración no existe para esta.
	if mandate.Grantee != claims.Subject {
		writeError(w, http.StatusNotFound, CodeMandateNotFound, "el mandato no existe")
		return
	}
	writeJSON(w, http.StatusOK, toMandateJSON(mandate))
}

// statusLabel pasa el estado interno al vocabulario de la API.
func statusLabel(status string) string {
	switch status {
	case oauth.ConsentPending:
		return "pending"
	case oauth.ConsentApproved:
		return "approved"
	case oauth.ConsentRejected:
		return "rejected"
	case oauth.ConsentExpired:
		return "expired"
	default:
		return "unknown"
	}
}
