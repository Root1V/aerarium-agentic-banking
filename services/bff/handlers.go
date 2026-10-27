package bff

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/aibank/aibank/clients/go/coreclient"
	corev1 "github.com/aibank/aibank/clients/go/corev1"
	"github.com/aibank/aibank/services/stepup"
)

// homeResponse es la pantalla principal en una sola llamada.
type homeResponse struct {
	Account accountJSON `json:"account"`
	// Balance es lo DISPONIBLE: lo retenido ya salió de aquí.
	Balance moneyJSON `json:"balance"`
	// Retained es lo que salió del saldo hacia pagos que todavía no se cobraron
	// ni volvieron. Cero si no hay ninguno; AUSENTE si no se pudo calcular. La
	// diferencia importa: "0 retenido" es una afirmación, y no se hace sin saberlo.
	Retained *moneyJSON `json:"retained,omitempty"`
	// Pending desglosa lo retenido por plataforma, de mayor a menor.
	Pending   []pendingGroupJSON `json:"pending,omitempty"`
	Movements []movementJSON     `json:"movements"`
	// HasMore dice si hay movimientos más antiguos que los de esta pantalla.
	// La app lo necesita para no rotular "10 pagos" una racha que sigue más
	// allá de lo que se cargó: diría "últimos 10 pagos", que es verdad.
	HasMore bool `json:"has_more"`
}

// pendingGroupJSON es lo retenido para una misma plataforma.
//
// Agrupado y no una fila por pago: un agente que paga por llamada deja cientos
// de retenciones, y lo que una persona necesita saber es cuánto y para quién.
type pendingGroupJSON struct {
	// Merchant es el nombre registrado de la integración en el banco, no uno que
	// ella elija: el mismo criterio que la pantalla de consentimiento. Vacío si
	// la retención no se hizo bajo un mandato.
	Merchant string    `json:"merchant"`
	Count    int64     `json:"count"`
	Amount   moneyJSON `json:"amount"`
	// Expired ya venció: sigue retenido hasta que pase el barrendero, pero no se
	// puede cobrar y vuelve al saldo en breve.
	Expired moneyJSON `json:"expired"`
	// LastExpiresAt: antes de esta hora, todo lo retenido o se cobra o vuelve.
	LastExpiresAt time.Time `json:"last_expires_at"`
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
			"materialized", balance.AmountMicros,
			"projected", balance.ProjectedMicros)
		writeError(w, http.StatusServiceUnavailable, "unavailable", "servicio no disponible, intenta de nuevo")
		return
	}

	statement, err := s.core.ListMovements(r.Context(), accountID, 10, "")
	if err != nil {
		s.writeCoreError(w, r, err)
		return
	}

	retained, pending := s.liveHolds(r.Context(), account.Id, account.Currency)

	writeJSON(w, http.StatusOK, homeResponse{
		Account:   accountJSON{ID: account.Id, Name: account.Name, Currency: account.Currency},
		Balance:   moneyJSON{AmountMicros: balance.AmountMicros, Currency: balance.Currency},
		Retained:  retained,
		Pending:   pending,
		Movements: s.toMovementsJSON(r.Context(), statement.Movements),
		HasMore:   statement.NextCursor != "",
	})
}

// liveHolds resume lo retenido en la cuenta.
//
// Si el core no responde, devuelve nil y la pantalla se arma sin el dato: un
// inicio sin el desglose es mejor que un inicio que no carga, y la app oculta la
// fila cuando el dato falta. Lo que nunca hace es contestar "0 retenido" sin
// saberlo.
func (s *Server) liveHolds(ctx context.Context, accountID, currency string) (*moneyJSON, []pendingGroupJSON) {
	groups, err := s.core.SummarizeLiveHolds(ctx, accountID)
	if err != nil {
		s.logger.WarnContext(ctx, "no se pudo resumir lo retenido; el inicio sale sin el dato",
			"account_id", accountID, "error", err)
		return nil, nil
	}

	retained := moneyJSON{Currency: currency}
	pending := make([]pendingGroupJSON, 0, len(groups))
	for _, g := range groups {
		if g.Currency != currency {
			// Una cuenta es de una sola moneda y el core rechaza autorizar en
			// otra, así que esto no debería ocurrir. Si ocurre, sumarlo mezclaría
			// monedas: se excluye y queda registrado.
			s.logger.ErrorContext(ctx, "retención en una moneda distinta a la de la cuenta",
				"account_id", accountID, "account_currency", currency, "hold_currency", g.Currency)
			continue
		}
		retained.AmountMicros += g.TotalMicros
		pending = append(pending, pendingGroupJSON{
			Merchant:      s.merchantName(ctx, g.Grantee),
			Count:         g.Count,
			Amount:        moneyJSON{AmountMicros: g.TotalMicros, Currency: g.Currency},
			Expired:       moneyJSON{AmountMicros: g.ExpiredMicros, Currency: g.Currency},
			LastExpiresAt: g.LastExpiresAt,
		})
	}
	return &retained, pending
}

// merchantName traduce el client_id de una integración a su nombre registrado.
//
// Si el registro no está disponible se devuelve el client_id: es menos amable,
// pero sigue identificando de verdad a quien cobra, que es lo que importa.
func (s *Server) merchantName(ctx context.Context, grantee string) string {
	if grantee == "" || s.consents == nil {
		return grantee
	}
	client, err := s.consents.FindClient(ctx, grantee)
	if err != nil || client.Name == "" {
		return grantee
	}
	return client.Name
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
	writeJSON(w, http.StatusOK, moneyJSON{AmountMicros: balance.AmountMicros, Currency: balance.Currency})
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
		Movements:  s.toMovementsJSON(r.Context(), statement.Movements),
		NextCursor: statement.NextCursor,
	})
}

// transferRequest es una transferencia entre cuentas del propio banco.
type transferRequest struct {
	FromAccountID string `json:"from_account_id"`
	ToAccountID   string `json:"to_account_id"`
	AmountMicros  int64  `json:"amount_micros"`
	Currency      string `json:"currency"`
	Description   string `json:"description"`
	// Confirmation llega en el segundo intento, cuando la operación pidió
	// confirmarse con el teléfono.
	Confirmation *transferConfirmation `json:"confirmation,omitempty"`
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
	if req.AmountMicros <= 0 {
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

	ok := s.stepUpTransfer(w, r, p, stepup.Transfer{
		CustomerID:     p.CustomerID,
		FromAccountID:  req.FromAccountID,
		ToAccountID:    req.ToAccountID,
		AmountMicros:   req.AmountMicros,
		Currency:       req.Currency,
		IdempotencyKey: idempotencyKey,
	}, destination.Name, req.Confirmation)
	if !ok {
		return
	}

	// La clave se prefija con el cliente: dos personas pueden usar la misma clave
	// sin colisionar, y nadie puede afectar la operación de otro adivinándola.
	key := "bff:transfer:" + p.CustomerID + ":" + idempotencyKey

	result, err := s.core.Post(r.Context(), key, "p2p_transfer", []coreclient.Entry{
		coreclient.Debit(req.FromAccountID, req.AmountMicros, req.Currency),
		coreclient.Credit(req.ToAccountID, req.AmountMicros, req.Currency),
	}, req.Description)
	if err != nil {
		s.writeCoreError(w, r, err)
		return
	}

	if s.stepUp != nil && p.Factor != "" {
		// Queda conocido —los envíos chicos ya no piden la cara— y arriba en los
		// recientes. Si no se pudo anotar, la próxima vez se vuelve a confirmar,
		// que es el lado seguro del error.
		if err := s.stepUp.policy.Remember(r.Context(), p.CustomerID, req.ToAccountID); err != nil {
			s.logger.ErrorContext(r.Context(), "anotar destinatario", "error", err)
		}
	}

	writeJSON(w, http.StatusCreated, transferResponse{
		TransactionID: result.TransactionID,
		Duplicate:     result.Replayed,
	})
}

// toMovementsJSON convierte movimientos del core al formato del canal.
func (s *Server) toMovementsJSON(ctx context.Context, movements []coreclient.Movement) []movementJSON {
	out := plainMovementsJSON(movements)

	// Los pagos hechos con un permiso se rotulan con su plataforma.
	var holds []string
	for _, m := range movements {
		if m.Kind == "authorization_hold" {
			holds = append(holds, m.TransactionID)
		}
	}
	if len(holds) == 0 {
		return out
	}
	grantees, err := s.core.LookupHoldGrantees(ctx, holds)
	if err != nil {
		// Sin el dato, el historial sale como antes: sin agrupar, pero correcto.
		s.logger.WarnContext(ctx, "no se pudo resolver la plataforma de los pagos", "error", err)
		return out
	}

	// Una consulta al registro por plataforma, no por pago: 120 micropagos de la
	// misma plataforma son 120 filas y un solo nombre.
	names := make(map[string]string)
	for i := range out {
		grantee, ok := grantees[out[i].ID]
		if !ok {
			continue
		}
		name, seen := names[grantee]
		if !seen {
			name = s.merchantName(ctx, grantee)
			names[grantee] = name
		}
		out[i].Merchant = name
	}
	return out
}

func plainMovementsJSON(movements []coreclient.Movement) []movementJSON {
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
			Amount:      moneyJSON{AmountMicros: m.AmountMicros, Currency: m.Currency},
			Sign:        sign,
			Kind:        m.Kind,
			Description: m.Description,
			PostedAt:    m.PostedAt,
		})
	}
	return out
}
