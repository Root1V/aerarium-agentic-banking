package bff

import (
	"errors"
	"net/http"
	"time"

	"github.com/aibank/aibank/clients/go/coreclient"
	"github.com/aibank/aibank/services/fx"
)

// Cambiar entre las cuentas propias, soles y dólares (F3-09), con el costo del
// cambio dicho en dinero. No pasa por la confirmación con la cara: el dinero no
// sale de la persona.

// WithFX habilita cambiar entre cuentas propias.
func (s *Server) WithFX(svc *fx.Service) *Server {
	s.fx = svc
	return s
}

func (s *Server) fxRoutes(mux *http.ServeMux) {
	mux.Handle("POST /v1/fx/quotes", s.requireAuth(s.handleFXQuote))
	mux.Handle("POST /v1/fx/conversions", s.requireAuth(s.handleFXConvert))
}

type fxQuoteJSON struct {
	QuoteID string    `json:"quote_id"`
	From    moneyJSON `json:"from"`
	To      moneyJSON `json:"to"`
	// Soles por dólar, en millonésimas: 3,38 es 3380000.
	ReferenceRate int64 `json:"reference_rate"`
	AppliedRate   int64 `json:"applied_rate"`
	// Cost: lo que se deja de recibir frente a la referencia, en la moneda que
	// se recibe.
	Cost      moneyJSON `json:"cost"`
	ExpiresAt time.Time `json:"expires_at"`
}

func toFXQuoteJSON(q *fx.Quote) fxQuoteJSON {
	return fxQuoteJSON{QuoteID: q.ID,
		From:          moneyJSON{AmountMicros: q.FromMicros, Currency: q.FromCurrency},
		To:            moneyJSON{AmountMicros: q.ToMicros, Currency: q.ToCurrency},
		ReferenceRate: q.ReferenceRate, AppliedRate: q.AppliedRate,
		Cost:      moneyJSON{AmountMicros: q.CostMicros, Currency: q.ToCurrency},
		ExpiresAt: q.ExpiresAt}
}

func (s *Server) handleFXQuote(w http.ResponseWriter, r *http.Request, p *Principal) {
	var body struct {
		FromAccountID string `json:"from_account_id"`
		ToAccountID   string `json:"to_account_id"`
		AmountMicros  int64  `json:"amount_micros"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.AmountMicros <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "el monto tiene que ser mayor que cero")
		return
	}
	q, err := s.fx.Quote(r.Context(), p.CustomerID, body.FromAccountID, body.ToAccountID, body.AmountMicros)
	if err != nil {
		s.fxError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toFXQuoteJSON(q))
}

func (s *Server) handleFXConvert(w http.ResponseWriter, r *http.Request, p *Principal) {
	var body struct {
		QuoteID string `json:"quote_id"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	q, result, err := s.fx.Convert(r.Context(), p.CustomerID, body.QuoteID)
	if err != nil {
		s.fxError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"transaction_id": result.TransactionID, "duplicate": result.Replayed, "quote": toFXQuoteJSON(q)})
}

func (s *Server) fxError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, fx.ErrNotFound), errors.Is(err, fx.ErrNotOwned):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, fx.ErrExpired):
		writeError(w, http.StatusConflict, "quote_expired", err.Error())
	case errors.Is(err, fx.ErrSameCurrency), errors.Is(err, fx.ErrPair):
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.Is(err, coreclient.ErrInsufficientFunds):
		writeError(w, http.StatusUnprocessableEntity, "insufficient_funds", "no te alcanza para cambiar eso")
	default:
		s.writeCoreError(w, r, err)
	}
}
