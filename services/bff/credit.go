package bff

import (
	"context"
	"errors"
	"net/http"

	"github.com/aibank/aibank/clients/go/coreclient"
	corev1 "github.com/aibank/aibank/clients/go/corev1"
	"github.com/aibank/aibank/services/credit"
)

// Tarjeta de crédito en el canal del titular: pedirla, verla, pasar dinero de
// ella a una cuenta propia y pagarla.

// WithCredit habilita la tarjeta de crédito.
func (s *Server) WithCredit(svc *credit.Service) *Server {
	s.credit = svc
	return s
}

func (s *Server) creditRoutes(mux *http.ServeMux) {
	mux.Handle("GET /v1/credit/card", s.requireAuth(s.handleCreditCard))
	mux.Handle("POST /v1/credit/applications", s.requireAuth(s.handleCreditApplication))
	mux.Handle("POST /v1/credit/card/draws", s.requireAuth(s.handleCreditDraw))
	mux.Handle("POST /v1/credit/card/payments", s.requireAuth(s.handleCreditPayment))
}

type creditCardJSON struct {
	ID          string    `json:"id"`
	Limit       moneyJSON `json:"limit"`
	Outstanding moneyJSON `json:"outstanding"`
	Available   moneyJSON `json:"available"`
	// AnnualRateBps es la TEA del contrato en puntos básicos: se muestra al
	// pedirla y en la tarjeta, como exige la norma de transparencia.
	AnnualRateBps int32  `json:"annual_rate_bps"`
	CycleDay      int32  `json:"cycle_day"`
	PaymentDays   int32  `json:"payment_days"`
	Status        string `json:"status"`
}

func toCreditCard(l *corev1.CreditLine) creditCardJSON {
	m := func(v *corev1.Money) moneyJSON { return moneyJSON{AmountMicros: v.AmountMicros, Currency: v.Currency} }
	status := "active"
	switch l.Status {
	case corev1.CreditLineStatus_CREDIT_LINE_STATUS_BLOCKED:
		status = "blocked"
	case corev1.CreditLineStatus_CREDIT_LINE_STATUS_CLOSED:
		status = "closed"
	}
	return creditCardJSON{
		ID: l.Id, Limit: m(l.Limit), Outstanding: m(l.Outstanding), Available: m(l.Available),
		AnnualRateBps: l.AnnualRateBps, CycleDay: l.CycleDay, PaymentDays: l.PaymentDays, Status: status,
	}
}

func (s *Server) handleCreditCard(w http.ResponseWriter, r *http.Request, p *Principal) {
	line, err := s.credit.Line(r.Context(), p.CustomerID)
	if err != nil {
		s.creditError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toCreditCard(line))
}

// handleCreditApplication evalúa al titular sobre una cuenta suya y, si se
// aprueba, abre la tarjeta. Nunca dice el puntaje: dice qué falta.
func (s *Server) handleCreditApplication(w http.ResponseWriter, r *http.Request, p *Principal) {
	var body struct {
		AccountID string `json:"account_id"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	account, err := s.authorizeAccount(r.Context(), p, body.AccountID)
	if err != nil {
		s.writeCoreError(w, r, err)
		return
	}
	d, err := s.credit.Apply(r.Context(), p.CustomerID, account.Id, account.Name)
	if err != nil {
		s.creditError(w, r, err)
		return
	}
	resp := map[string]any{
		"application_id":    d.ApplicationID,
		"outcome":           d.Outcome,
		"reasons":           d.Reasons,
		"synthetic_history": d.SyntheticHistory,
	}
	if d.Line != nil {
		resp["card"] = toCreditCard(d.Line)
	}
	writeJSON(w, http.StatusOK, resp)
}

type creditMoveRequest struct {
	AccountID    string `json:"account_id"`
	AmountMicros int64  `json:"amount_micros"`
}

// handleCreditDraw pasa dinero de la tarjeta a una cuenta propia.
func (s *Server) handleCreditDraw(w http.ResponseWriter, r *http.Request, p *Principal) {
	s.creditMove(w, r, p, s.credit.Draw)
}

// handleCreditPayment paga la tarjeta desde una cuenta propia.
func (s *Server) handleCreditPayment(w http.ResponseWriter, r *http.Request, p *Principal) {
	s.creditMove(w, r, p, s.credit.Pay)
}

func (s *Server) creditMove(w http.ResponseWriter, r *http.Request, p *Principal,
	move func(ctx context.Context, customerID, accountID string, amount int64, key string) (*coreclient.PostResult, error)) {
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		writeError(w, http.StatusBadRequest, "missing_idempotency_key", "se requiere el encabezado Idempotency-Key")
		return
	}
	var body creditMoveRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.AmountMicros <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "el monto debe ser positivo")
		return
	}
	// La cuenta que recibe o que paga tiene que ser del titular.
	if _, err := s.authorizeAccount(r.Context(), p, body.AccountID); err != nil {
		s.writeCoreError(w, r, err)
		return
	}
	result, err := move(r.Context(), p.CustomerID, body.AccountID, body.AmountMicros, key)
	if err != nil {
		s.creditError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, transferResponse{TransactionID: result.TransactionID, Duplicate: result.Replayed})
}

func (s *Server) creditError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, credit.ErrNoLine):
		writeError(w, http.StatusNotFound, "no_credit_card", "todavía no tienes tarjeta de crédito")
	case errors.Is(err, credit.ErrAlreadyHasLine):
		writeError(w, http.StatusConflict, "already_has_card", "ya tienes una tarjeta de crédito")
	case errors.Is(err, credit.ErrRiskUnavailable):
		s.logger.ErrorContext(r.Context(), "motor de riesgo", "error", err)
		writeError(w, http.StatusServiceUnavailable, "unavailable", "no pudimos evaluar tu solicitud; inténtalo más tarde")
	default:
		s.writeCoreError(w, r, err)
	}
}
