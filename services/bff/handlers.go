package bff

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/aibank/aibank/clients/go/coreclient"
	corev1 "github.com/aibank/aibank/clients/go/corev1"
)

// homeResponse es la pantalla principal en una sola llamada.
type homeResponse struct {
	Account   accountJSON    `json:"account"`
	Balance   moneyJSON      `json:"balance"`
	Movements []movementJSON `json:"movements"`
}

// handleHome arma la pantalla principal: cuenta, saldo y últimos movimientos.
//
// Existe para que el móvil haga UN viaje en vez de tres. En redes móviles de la
// región esa diferencia se nota más que cualquier optimización del servidor.
func (s *Server) handleHome(w http.ResponseWriter, r *http.Request, p *Principal) {
	accountID := r.URL.Query().Get("account_id")
	if accountID == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "falta account_id")
		return
	}

	account, err := s.authorizeAccount(r.Context(), p, accountID)
	if err != nil {
		s.writeCoreError(w, r, err)
		return
	}

	balance, err := s.core.GetBalance(r.Context(), accountID)
	if err != nil {
		s.writeCoreError(w, r, err)
		return
	}
	if !balance.Consistent() {
		// El saldo materializado no cuadra con el ledger: es un incidente
		// contable. Se prefiere no responder antes que mostrar una cifra dudosa.
		s.logger.ErrorContext(r.Context(), "saldo inconsistente con el ledger",
			"account_id", accountID,
			"materialized", balance.AmountMinor,
			"projected", balance.ProjectedMinor)
		writeError(w, http.StatusServiceUnavailable, "unavailable", "servicio no disponible, intenta de nuevo")
		return
	}

	statement, err := s.core.ListMovements(r.Context(), accountID, 10, "")
	if err != nil {
		s.writeCoreError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, homeResponse{
		Account:   accountJSON{ID: account.Id, Name: account.Name, Currency: account.Currency},
		Balance:   moneyJSON{AmountMinor: balance.AmountMinor, Currency: balance.Currency},
		Movements: toMovementsJSON(statement.Movements),
	})
}

func (s *Server) handleBalance(w http.ResponseWriter, r *http.Request, p *Principal) {
	accountID := r.PathValue("accountID")
	if _, err := s.authorizeAccount(r.Context(), p, accountID); err != nil {
		s.writeCoreError(w, r, err)
		return
	}

	balance, err := s.core.GetBalance(r.Context(), accountID)
	if err != nil {
		s.writeCoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, moneyJSON{AmountMinor: balance.AmountMinor, Currency: balance.Currency})
}

type movementsResponse struct {
	Movements []movementJSON `json:"movements"`
	// NextCursor vacío significa que no hay más páginas.
	NextCursor string `json:"next_cursor"`
}

func (s *Server) handleMovements(w http.ResponseWriter, r *http.Request, p *Principal) {
	accountID := r.PathValue("accountID")
	if _, err := s.authorizeAccount(r.Context(), p, accountID); err != nil {
		s.writeCoreError(w, r, err)
		return
	}

	limit := int32(25)
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeError(w, http.StatusBadRequest, "invalid_request", "limit debe ser un entero positivo")
			return
		}
		limit = int32(parsed)
	}

	statement, err := s.core.ListMovements(r.Context(), accountID, limit, r.URL.Query().Get("cursor"))
	if err != nil {
		s.writeCoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, movementsResponse{
		Movements:  toMovementsJSON(statement.Movements),
		NextCursor: statement.NextCursor,
	})
}

// transferRequest es una transferencia entre cuentas del propio banco.
type transferRequest struct {
	FromAccountID string `json:"from_account_id"`
	ToAccountID   string `json:"to_account_id"`
	AmountMinor   int64  `json:"amount_minor"`
	Currency      string `json:"currency"`
	Description   string `json:"description"`
}

type transferResponse struct {
	TransactionID string `json:"transaction_id"`
	// Duplicate indica que la clave ya se había usado: la operación no se repitió.
	Duplicate bool `json:"duplicate"`
}

// handleTransfer mueve dinero entre dos cuentas del banco.
//
// Exige el encabezado Idempotency-Key. No es una comodidad: en un móvil, un toque
// doble o un reintento por red intermitente son la norma, y sin clave estable cada
// reintento sería una transferencia nueva.
func (s *Server) handleTransfer(w http.ResponseWriter, r *http.Request, p *Principal) {
	idempotencyKey := r.Header.Get("Idempotency-Key")
	if idempotencyKey == "" {
		writeError(w, http.StatusBadRequest, "missing_idempotency_key",
			"se requiere el encabezado Idempotency-Key")
		return
	}

	var req transferRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "cuerpo inválido")
		return
	}
	if req.AmountMinor <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "el monto debe ser positivo")
		return
	}
	if req.FromAccountID == req.ToAccountID {
		writeError(w, http.StatusBadRequest, "invalid_request", "origen y destino no pueden coincidir")
		return
	}

	// Solo se valida la titularidad del ORIGEN: nadie puede enviar dinero desde
	// una cuenta ajena. El destino no se valida como propio — precisamente el
	// caso de uso es enviarle a otra persona.
	origin, err := s.authorizeAccount(r.Context(), p, req.FromAccountID)
	if err != nil {
		s.writeCoreError(w, r, err)
		return
	}

	destination, err := s.core.GetAccountByID(r.Context(), req.ToAccountID)
	if err != nil {
		s.writeCoreError(w, r, err)
		return
	}
	if destination.Owner != corev1.AccountOwner_ACCOUNT_OWNER_CUSTOMER {
		// Una cuenta interna del banco no es un destino válido desde el canal.
		writeError(w, http.StatusNotFound, "not_found", "no encontrado")
		return
	}
	if destination.Currency != origin.Currency || req.Currency != origin.Currency {
		writeError(w, http.StatusBadRequest, "currency_mismatch", "las monedas no coinciden")
		return
	}

	// La clave se prefija con el cliente: dos personas pueden usar la misma clave
	// sin colisionar, y nadie puede afectar la operación de otro adivinándola.
	key := "bff:transfer:" + p.CustomerID + ":" + idempotencyKey

	result, err := s.core.Post(r.Context(), key, "p2p_transfer", []coreclient.Entry{
		coreclient.Debit(req.FromAccountID, req.AmountMinor, req.Currency),
		coreclient.Credit(req.ToAccountID, req.AmountMinor, req.Currency),
	}, req.Description)
	if err != nil {
		s.writeCoreError(w, r, err)
		return
	}

	writeJSON(w, http.StatusCreated, transferResponse{
		TransactionID: result.TransactionID,
		Duplicate:     result.Replayed,
	})
}

// toMovementsJSON convierte movimientos del core al formato del canal.
func toMovementsJSON(movements []coreclient.Movement) []movementJSON {
	out := make([]movementJSON, 0, len(movements))
	for _, m := range movements {
		// La cuenta del cliente es un pasivo del banco: un crédito le SUMA saldo
		// y un débito se lo resta. Se resuelve aquí para que la app no tenga que
		// razonar en términos contables.
		sign := 1
		if m.Direction == corev1.Direction_DIRECTION_DEBIT {
			sign = -1
		}
		out = append(out, movementJSON{
			ID:          m.TransactionID,
			Cursor:      m.Cursor,
			Amount:      moneyJSON{AmountMinor: m.AmountMinor, Currency: m.Currency},
			Sign:        sign,
			Kind:        m.Kind,
			Description: m.Description,
			PostedAt:    m.PostedAt,
		})
	}
	return out
}
