package bff

import (
	"context"
	"errors"
	"net/http"
	"time"

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
	mux.Handle("GET /v1/credit/card/statements", s.requireAuth(s.handleCreditStatements))
	// Solo en el sandbox: el ciclo dura un mes y nadie puede esperar tanto para
	// probarlo.
	if s.sandbox != nil {
		mux.Handle("POST /v1/sandbox/credit/close-cycle", s.requireAuth(s.handleSandboxCloseCycle))
		mux.Handle("POST /v1/sandbox/credit/pass-due-date", s.requireAuth(s.handleSandboxPassDueDate))
	}
}

type statementJSON struct {
	PeriodStart    time.Time `json:"period_start"`
	PeriodEnd      time.Time `json:"period_end"`
	Opening        moneyJSON `json:"opening"`
	Charges        moneyJSON `json:"charges"`
	Payments       moneyJSON `json:"payments"`
	Interest       moneyJSON `json:"interest"`
	Closing        moneyJSON `json:"closing"`
	MinimumPayment moneyJSON `json:"minimum_payment"`
	DueDate        string    `json:"due_date"`
	PaidSinceCut   moneyJSON `json:"paid_since_cut"`
	// Status resume lo que la persona necesita saber: "paid" (pagó el total),
	// "minimum_paid", "pending" (todavía a tiempo) o "past_due" (venció sin el
	// mínimo).
	Status string `json:"status"`
}

func toStatement(st *corev1.CreditStatement, today time.Time) statementJSON {
	m := func(v *corev1.Money) moneyJSON { return moneyJSON{AmountMicros: v.AmountMicros, Currency: v.Currency} }
	paid := st.PaidSinceCut.AmountMicros
	status := "pending"
	switch {
	case paid >= st.Closing.AmountMicros:
		status = "paid"
	case paid >= st.MinimumPayment.AmountMicros:
		status = "minimum_paid"
	case today.Format(time.DateOnly) > st.DueDate:
		status = "past_due"
	}
	return statementJSON{
		PeriodStart: st.PeriodStart.AsTime(), PeriodEnd: st.PeriodEnd.AsTime(),
		Opening: m(st.Opening), Charges: m(st.Charges), Payments: m(st.Payments), Interest: m(st.Interest),
		Closing: m(st.Closing), MinimumPayment: m(st.MinimumPayment), DueDate: st.DueDate,
		PaidSinceCut: m(st.PaidSinceCut), Status: status,
	}
}

func (s *Server) handleCreditStatements(w http.ResponseWriter, r *http.Request, p *Principal) {
	statements, err := s.credit.Statements(r.Context(), p.CustomerID, 12)
	if err != nil {
		s.creditError(w, r, err)
		return
	}
	out := make([]statementJSON, 0, len(statements))
	for _, st := range statements {
		out = append(out, toStatement(st, time.Now()))
	}
	writeJSON(w, http.StatusOK, map[string]any{"statements": out})
}

func (s *Server) handleSandboxCloseCycle(w http.ResponseWriter, r *http.Request, p *Principal) {
	st, err := s.credit.CloseCycleNow(r.Context(), p.CustomerID)
	if err != nil {
		s.creditError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toStatement(st, time.Now()))
}

func (s *Server) handleSandboxPassDueDate(w http.ResponseWriter, r *http.Request, p *Principal) {
	line, err := s.credit.PassDueDate(r.Context(), p.CustomerID)
	if err != nil {
		s.creditError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toCreditCard(line, nil))
}

type creditCardJSON struct {
	// Statement es el último estado de cuenta, si ya hubo un corte.
	Statement   *statementJSON `json:"statement,omitempty"`
	ID          string         `json:"id"`
	Limit       moneyJSON      `json:"limit"`
	Outstanding moneyJSON      `json:"outstanding"`
	Available   moneyJSON      `json:"available"`
	// AnnualRateBps es la TEA del contrato en puntos básicos: se muestra al
	// pedirla y en la tarjeta, como exige la norma de transparencia.
	AnnualRateBps int32  `json:"annual_rate_bps"`
	CycleDay      int32  `json:"cycle_day"`
	PaymentDays   int32  `json:"payment_days"`
	Status        string `json:"status"`
}

func toCreditCard(l *corev1.CreditLine, latest *corev1.CreditStatement) creditCardJSON {
	m := func(v *corev1.Money) moneyJSON { return moneyJSON{AmountMicros: v.AmountMicros, Currency: v.Currency} }
	status := "active"
	switch l.Status {
	case corev1.CreditLineStatus_CREDIT_LINE_STATUS_BLOCKED:
		status = "blocked"
	case corev1.CreditLineStatus_CREDIT_LINE_STATUS_CLOSED:
		status = "closed"
	}
	var statement *statementJSON
	if latest != nil {
		st := toStatement(latest, time.Now())
		statement = &st
	}
	return creditCardJSON{
		Statement: statement,
		ID:        l.Id, Limit: m(l.Limit), Outstanding: m(l.Outstanding), Available: m(l.Available),
		AnnualRateBps: l.AnnualRateBps, CycleDay: l.CycleDay, PaymentDays: l.PaymentDays, Status: status,
	}
}

func (s *Server) handleCreditCard(w http.ResponseWriter, r *http.Request, p *Principal) {
	line, err := s.credit.Line(r.Context(), p.CustomerID)
	if err != nil {
		s.creditError(w, r, err)
		return
	}
	var latest *corev1.CreditStatement
	if statements, err := s.credit.Statements(r.Context(), p.CustomerID, 1); err == nil && len(statements) > 0 {
		latest = statements[0]
	}
	writeJSON(w, http.StatusOK, toCreditCard(line, latest))
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
		resp["card"] = toCreditCard(d.Line, nil)
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
	case errors.Is(err, credit.ErrNoStatement):
		writeError(w, http.StatusConflict, "no_statement", "la tarjeta todavía no tiene estado de cuenta")
	case errors.Is(err, credit.ErrAlreadyHasLine):
		writeError(w, http.StatusConflict, "already_has_card", "ya tienes una tarjeta de crédito")
	case errors.Is(err, credit.ErrRiskUnavailable):
		s.logger.ErrorContext(r.Context(), "motor de riesgo", "error", err)
		writeError(w, http.StatusServiceUnavailable, "unavailable", "no pudimos evaluar tu solicitud; inténtalo más tarde")
	default:
		s.writeCoreError(w, r, err)
	}
}
