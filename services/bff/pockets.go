package bff

import (
	"errors"
	"net/http"

	"github.com/aibank/aibank/clients/go/coreclient"
	"github.com/aibank/aibank/services/pockets"
)

// Los bolsillos (F3-02): dinero apartado dentro de la cuenta.
//
// Moverlo entre la cuenta y sus bolsillos no pasa por la capa de control ni
// pide la cara: el dinero no sale de la persona. Lo que sí no se puede es
// mandarlo a otro desde un bolsillo (ver handleTransfer).

// WithPockets habilita los bolsillos.
func (s *Server) WithPockets(svc *pockets.Service) *Server {
	s.pockets = svc
	return s
}

func (s *Server) pocketRoutes(mux *http.ServeMux) {
	mux.Handle("GET /v1/accounts/{accountID}/pockets", s.requireAuth(s.handleListPockets))
	mux.Handle("POST /v1/accounts/{accountID}/pockets", s.requireAuth(s.handleOpenPocket))
	mux.Handle("POST /v1/pockets/{pocketID}/deposit", s.requireAuth(s.handlePocketMove(true)))
	mux.Handle("POST /v1/pockets/{pocketID}/withdraw", s.requireAuth(s.handlePocketMove(false)))
	mux.Handle("DELETE /v1/pockets/{pocketID}", s.requireAuth(s.handleClosePocket))
}

type pocketJSON struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Purpose string    `json:"purpose"`
	Balance moneyJSON `json:"balance"`
}

func toPocketJSON(p pockets.Pocket) pocketJSON {
	return pocketJSON{ID: p.ID, Name: p.Name, Purpose: p.Purpose,
		Balance: moneyJSON{AmountMicros: p.BalanceMicros, Currency: p.Currency}}
}

func (s *Server) handleListPockets(w http.ResponseWriter, r *http.Request, p *Principal) {
	accountID := r.PathValue("accountID")
	if _, err := s.authorizeAccount(r.Context(), p, accountID); err != nil {
		s.writeCoreError(w, r, err)
		return
	}
	list, err := s.pockets.List(r.Context(), p.CustomerID, accountID)
	if err != nil {
		s.writeCoreError(w, r, err)
		return
	}
	out := []pocketJSON{}
	for _, pk := range list {
		out = append(out, toPocketJSON(pk))
	}
	writeJSON(w, http.StatusOK, map[string]any{"pockets": out})
}

func (s *Server) handleOpenPocket(w http.ResponseWriter, r *http.Request, p *Principal) {
	accountID := r.PathValue("accountID")
	if _, err := s.authorizeAccount(r.Context(), p, accountID); err != nil {
		s.writeCoreError(w, r, err)
		return
	}
	var body struct {
		Name    string `json:"name"`
		Purpose string `json:"purpose"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Purpose == "" {
		body.Purpose = pockets.PurposeGeneral
	}
	pocket, err := s.pockets.Open(r.Context(), p.CustomerID, accountID, body.Name, body.Purpose)
	if err != nil {
		s.pocketError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toPocketJSON(*pocket))
}

func (s *Server) handlePocketMove(in bool) func(http.ResponseWriter, *http.Request, *Principal) {
	return func(w http.ResponseWriter, r *http.Request, p *Principal) {
		key := r.Header.Get("Idempotency-Key")
		if key == "" {
			writeError(w, http.StatusBadRequest, "invalid_request", "falta Idempotency-Key")
			return
		}
		var body struct {
			AmountMicros int64 `json:"amount_micros"`
		}
		if !decodeJSON(w, r, &body) {
			return
		}
		if body.AmountMicros <= 0 {
			writeError(w, http.StatusBadRequest, "invalid_request", "el monto tiene que ser mayor que cero")
			return
		}
		result, err := s.pockets.Move(r.Context(), p.CustomerID, r.PathValue("pocketID"), key, in, body.AmountMicros)
		if err != nil {
			s.pocketError(w, r, err)
			return
		}
		writeJSON(w, http.StatusCreated, transferResponse{TransactionID: result.TransactionID, Duplicate: result.Replayed})
	}
}

func (s *Server) handleClosePocket(w http.ResponseWriter, r *http.Request, p *Principal) {
	if err := s.pockets.Close(r.Context(), p.CustomerID, r.PathValue("pocketID")); err != nil {
		s.pocketError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) pocketError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, pockets.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, pockets.ErrBadName), errors.Is(err, pockets.ErrBadPurpose):
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.Is(err, pockets.ErrCardPocket), errors.Is(err, pockets.ErrTooMany),
		errors.Is(err, pockets.ErrNotAnAccount):
		writeError(w, http.StatusConflict, "pocket_conflict", err.Error())
	case errors.Is(err, coreclient.ErrInsufficientFunds):
		writeError(w, http.StatusUnprocessableEntity, "insufficient_funds", "no te alcanza para apartar eso")
	default:
		s.writeCoreError(w, r, err)
	}
}
