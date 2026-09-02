package mercatus

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aibank/aibank/clients/go/coreclient"
	corev1 "github.com/aibank/aibank/clients/go/corev1"
	"github.com/aibank/aibank/services/oauth"
	"github.com/google/uuid"
)

// Tamaño máximo de un cuerpo. Ninguna petición del contrato pasa de unos cientos
// de bytes; el límite evita que un cuerpo enorme consuma memoria del servicio.
const maxBodyBytes = 64 * 1024

// ---------------------------------------------------------------- cuentas

type openAccountRequest struct {
	OwnerReference string `json:"owner_reference"`
	Currency       string `json:"currency"`
	DisplayName    string `json:"display_name"`
	// InitialBalance solo se admite en sandbox: es lo que permite probar de verdad
	// el camino de insufficient_funds abriendo una cuenta con $0.10.
	InitialBalance int64 `json:"initial_balance,omitempty"`
}

type accountResponse struct {
	AccountID string `json:"account_id"`
	Currency  string `json:"currency"`
	Status    string `json:"status"`
}

func (s *Server) handleOpenAccount(w http.ResponseWriter, r *http.Request, claims *oauth.Claims) {
	var req openAccountRequest
	if !decodeBody(w, r, &req) {
		return
	}

	if req.OwnerReference == "" {
		writeError(w, http.StatusBadRequest, CodeMalformedRequest, "owner_reference es obligatorio")
		return
	}
	if len(req.Currency) != 3 {
		writeError(w, http.StatusBadRequest, CodeMalformedRequest, "currency debe ser ISO-4217 de 3 letras")
		return
	}
	product, supported := s.cfg.ProductByCurrency[req.Currency]
	if !supported {
		// Abrir la cuenta bajo el producto de otra moneda mezclaría dos monedas en
		// un mismo saldo. Se rechaza con la lista de las que sí se admiten.
		writeError(w, http.StatusUnprocessableEntity, CodeCurrencyMismatch,
			"moneda no disponible; admitidas: "+strings.Join(s.supportedCurrencies(), ", "))
		return
	}
	if req.InitialBalance != 0 && !s.cfg.Sandbox {
		// En producción el dinero entra por el fondeo de la cuenta maestra, no por
		// un campo de la petición que abre la cuenta.
		writeError(w, http.StatusBadRequest, CodeMalformedRequest,
			"initial_balance solo se admite en sandbox")
		return
	}

	// Si el agente ya tiene cuenta en esta moneda, se devuelve esa. Es la
	// idempotencia que pide el contrato: un reintento tras un timeout no puede
	// abrir una segunda cuenta y partir el saldo del agente en dos.
	if sub, found, err := s.store.FindAccount(r.Context(), claims.Subject, req.OwnerReference, req.Currency); err != nil {
		s.log.ErrorContext(r.Context(), "buscar sub-cuenta", "error", err)
		writeError(w, http.StatusInternalServerError, CodeServerError, "error interno")
		return
	} else if found {
		writeJSON(w, http.StatusOK, accountResponse{
			AccountID: encodeID(accountPrefix, sub.AccountID),
			Currency:  sub.Currency,
			Status:    "active",
		})
		return
	}

	// El código de la cuenta en el core es DETERMINISTA a partir de la
	// integración, el agente y la moneda. Con eso el core es la segunda red de
	// seguridad: si el proceso muere entre abrir la cuenta y registrar el enlace,
	// el reintento choca contra el código único y recupera la misma cuenta en vez
	// de abrir otra.
	code := accountCode(claims.Subject, req.OwnerReference, req.Currency)
	account, err := s.core.OpenCustomerAccount(r.Context(), code, displayNameOr(req), uuid.NewString(), product)
	if errors.Is(err, coreclient.ErrAlreadyExists) {
		account, err = s.core.GetAccount(r.Context(), code)
	}
	if err != nil {
		s.log.ErrorContext(r.Context(), "abrir cuenta en el core", "error", err, "code", code)
		coreError(w, err)
		return
	}

	if _, _, err := s.store.LinkAccount(r.Context(), claims.Subject, oauth.SubAccount{
		AccountID:      account.Id,
		OwnerReference: req.OwnerReference,
		Currency:       req.Currency,
		DisplayName:    req.DisplayName,
	}); err != nil {
		s.log.ErrorContext(r.Context(), "enlazar sub-cuenta", "error", err)
		writeError(w, http.StatusInternalServerError, CodeServerError, "error interno")
		return
	}

	if req.InitialBalance > 0 {
		if err := s.fundSandboxAccount(r, account.Id, req.InitialBalance, req.Currency); err != nil {
			s.log.ErrorContext(r.Context(), "fondear cuenta de sandbox", "error", err)
			coreError(w, err)
			return
		}
	}

	writeJSON(w, http.StatusCreated, accountResponse{
		AccountID: encodeID(accountPrefix, account.Id),
		Currency:  account.Currency,
		Status:    "active",
	})
}

// accountCode compone el código de la cuenta en el core.
//
// El `owner_reference` va hasheado y no en claro: es un identificador del socio
// que puede contener cualquier carácter, y el código de cuenta acaba en logs y
// en reportes contables. El hash lo hace de longitud fija y sin sorpresas, y
// sigue siendo determinista, que es lo único que se le pide.
func accountCode(clientID, ownerReference, currency string) string {
	sum := sha256.Sum256([]byte(clientID + "\x00" + ownerReference + "\x00" + currency))
	return "MRC-" + hex.EncodeToString(sum[:12])
}

// supportedCurrencies devuelve las monedas configuradas, ordenadas para que el
// mensaje de error sea estable entre peticiones.
func (s *Server) supportedCurrencies() []string {
	out := make([]string, 0, len(s.cfg.ProductByCurrency))
	for currency := range s.cfg.ProductByCurrency {
		out = append(out, currency)
	}
	sort.Strings(out)
	return out
}

func displayNameOr(req openAccountRequest) string {
	if req.DisplayName != "" {
		return req.DisplayName
	}
	return "Agente " + req.OwnerReference
}

// fundSandboxAccount acredita saldo inicial contra la caja del sandbox.
func (s *Server) fundSandboxAccount(r *http.Request, accountID string, amount int64, currency string) error {
	if s.cfg.SandboxCashAccountID == "" {
		return errors.New("sandbox sin cuenta de caja configurada")
	}
	_, err := s.core.Post(r.Context(), "sandbox-fund-"+accountID, "sandbox_funding", []coreclient.Entry{
		coreclient.Debit(s.cfg.SandboxCashAccountID, amount, currency),
		coreclient.Credit(accountID, amount, currency),
	}, "saldo inicial de sandbox")
	return err
}

type transactionJSON struct {
	Cursor        string `json:"cursor"`
	TransactionID string `json:"transaction_id"`
	Amount        int64  `json:"amount"`
	Currency      string `json:"currency"`
	// Direction es "credit" si el movimiento suma en la cuenta consultada y
	// "debit" si resta. Se resuelve aquí para que el cliente no tenga que conocer
	// la contabilidad de doble partida.
	Direction string    `json:"direction"`
	Kind      string    `json:"kind"`
	PostedAt  time.Time `json:"posted_at"`
}

type transactionsResponse struct {
	AccountID    string            `json:"account_id"`
	Transactions []transactionJSON `json:"transactions"`
	NextCursor   string            `json:"next_cursor,omitempty"`
}

func (s *Server) handleTransactions(w http.ResponseWriter, r *http.Request, claims *oauth.Claims) {
	accountID, ok := s.resolveOwnedAccount(w, r, claims, r.PathValue("accountID"))
	if !ok {
		return
	}

	limit := int32(50)
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 || n > 200 {
			writeError(w, http.StatusBadRequest, CodeMalformedRequest, "limit debe estar entre 1 y 200")
			return
		}
		limit = int32(n)
	}

	statement, err := s.core.ListMovements(r.Context(), accountID, limit, r.URL.Query().Get("cursor"))
	if err != nil {
		coreError(w, err)
		return
	}

	out := make([]transactionJSON, 0, len(statement.Movements))
	for _, m := range statement.Movements {
		direction := "debit"
		if m.Direction == corev1.Direction_DIRECTION_CREDIT {
			direction = "credit"
		}
		out = append(out, transactionJSON{
			Cursor:        m.Cursor,
			TransactionID: m.TransactionID,
			Amount:        m.AmountMicros,
			Currency:      m.Currency,
			Direction:     direction,
			Kind:          m.Kind,
			PostedAt:      m.PostedAt,
		})
	}

	writeJSON(w, http.StatusOK, transactionsResponse{
		AccountID:    encodeID(accountPrefix, accountID),
		Transactions: out,
		NextCursor:   statement.NextCursor,
	})
}

// ---------------------------------------------------------------- autorizaciones

type authorizeRequest struct {
	PayerAccountID string `json:"payer_account_id"`
	PayeeAccountID string `json:"payee_account_id"`
	Amount         int64  `json:"amount"`
	Currency       string `json:"currency"`
	// MandateID selecciona el MODELO B: la cuenta pagadora es de un cliente del
	// banco y el pago se ampara en el permiso que ese titular otorgó.
	//
	// Su ausencia significa modelo A: la cuenta pagadora es una sub-cuenta de la
	// propia integración. Es un campo y no dos endpoints porque el contrato de
	// pago es idéntico en los dos modelos — lo único que cambia es de dónde sale
	// la autoridad para mover el dinero.
	MandateID string `json:"mandate_id,omitempty"`
}

type authorizationResponse struct {
	AuthorizationID string     `json:"authorization_id"`
	Status          string     `json:"status"`
	PayerAccountID  string     `json:"payer_account_id,omitempty"`
	PayeeAccountID  string     `json:"payee_account_id,omitempty"`
	Amount          int64      `json:"amount,omitempty"`
	Currency        string     `json:"currency,omitempty"`
	ExpiresAt       *time.Time `json:"expires_at,omitempty"`
	SettledAt       *time.Time `json:"settled_at,omitempty"`
}

func (s *Server) handleAuthorize(w http.ResponseWriter, r *http.Request, claims *oauth.Claims) {
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		// Rechazar es más seguro que procesar: sin clave, un reintento por timeout
		// de red se convierte en un segundo cobro y nadie puede distinguirlo.
		writeError(w, http.StatusBadRequest, CodeMalformedRequest,
			"falta el encabezado Idempotency-Key")
		return
	}

	var req authorizeRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if req.Amount <= 0 {
		writeError(w, http.StatusBadRequest, CodeMalformedRequest, "amount debe ser mayor que cero")
		return
	}
	if len(req.Currency) != 3 {
		writeError(w, http.StatusBadRequest, CodeMalformedRequest, "currency debe ser ISO-4217 de 3 letras")
		return
	}

	// El receptor siempre tiene que ser una cuenta de la integración: un mandato
	// autoriza a SACAR dinero de la cuenta del titular, nunca a elegir libremente
	// dónde termina. Sin esta regla, un permiso otorgado para pagarle a un agente
	// serviría para mandar el dinero a cualquier parte.
	payee, ok := s.resolveOwnedAccount(w, r, claims, req.PayeeAccountID)
	if !ok {
		return
	}

	var payer string
	if req.MandateID != "" {
		payer, ok = s.resolveMandateAccount(w, r, claims, req.MandateID, req.PayerAccountID)
	} else {
		payer, ok = s.resolveOwnedAccount(w, r, claims, req.PayerAccountID)
	}
	if !ok {
		return
	}

	if payer == payee {
		writeError(w, http.StatusUnprocessableEntity, CodeSameAccount,
			"pagador y receptor no pueden ser la misma cuenta")
		return
	}

	// El límite por cuenta se cobra sobre el PAGADOR: es quien pierde dinero y
	// quien tiene que quedar protegido de un agente en bucle.
	if allowed, retryAfter := s.byAccount.Allow(payer); !allowed {
		s.tooManyRequests(w, retryAfter)
		return
	}

	// La clave del core se compone con el client_id: dos integraciones distintas
	// que elijan la misma clave son operaciones distintas y no deben colapsar.
	coreKey := claims.Subject + ":" + key

	var result *coreclient.AuthorizeResult
	var err error
	if req.MandateID != "" {
		// El core comprueba el permiso y retiene en la MISMA transacción: entre
		// "tiene permiso" y "se retuvo" no cabe una revocación.
		result, _, err = s.core.AuthorizeUnderMandate(r.Context(),
			req.MandateID, coreKey, payer, payee, req.Amount, req.Currency)
	} else {
		result, err = s.core.Authorize(r.Context(),
			coreKey, payer, payee, req.Amount, req.Currency, 0)
	}
	if err != nil {
		mandateError(w, err)
		return
	}

	auth := result.Authorization
	status := http.StatusCreated
	if result.Replayed {
		// 200 y no 201: no se creó nada. El encabezado lo hace distinguible en los
		// logs del cliente sin que tenga que comparar cuerpos.
		status = http.StatusOK
		w.Header().Set("Idempotent-Replay", "true")
	}

	expires := auth.ExpiresAt
	writeJSON(w, status, authorizationResponse{
		AuthorizationID: encodeID(authorizationPrefix, auth.ID),
		Status:          statusName(auth.Status),
		ExpiresAt:       &expires,
	})
}

func (s *Server) handleCapture(w http.ResponseWriter, r *http.Request, claims *oauth.Claims) {
	auth, ok := s.resolveOwnedAuthorization(w, r, claims)
	if !ok {
		return
	}

	captured, err := s.core.Capture(r.Context(), auth.ID)
	if err != nil {
		if errors.Is(err, coreclient.ErrInvalidState) {
			// El core rechaza capturar una retención ya liberada. Para el contrato
			// eso es un conflicto de estado, no una captura repetida: capturar dos
			// veces SÍ funciona y devuelve la misma captura.
			writeError(w, http.StatusConflict, CodeAlreadyRefunded,
				"la autorización ya no admite captura")
			return
		}
		coreError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, authorizationResponse{
		AuthorizationID: encodeID(authorizationPrefix, captured.ID),
		Status:          statusName(captured.Status),
		SettledAt:       captured.SettledAt,
	})
}

// refundRequest permite devolver menos que el total.
//
// El cuerpo es OPCIONAL: sin él, o con `amount` en cero, se devuelve todo lo que
// quede sin reembolsar. Así el reembolso total —que es el caso normal— sigue
// siendo una llamada sin cuerpo, y los clientes escritos antes de que esto
// existiera no cambian una línea.
type refundRequest struct {
	Amount int64 `json:"amount,omitempty"`
}

type refundResponse struct {
	AuthorizationID string `json:"authorization_id"`
	Status          string `json:"status"`
	// Cuánto se devolvió en ESTA operación.
	Refunded int64 `json:"refunded"`
	// Acumulado devuelto y cuánto queda por devolver, para que la operación de
	// soporte no tenga que llevar la cuenta por su lado.
	RefundedTotal int64  `json:"refunded_total"`
	Refundable    int64  `json:"refundable"`
	Currency      string `json:"currency"`
}

func (s *Server) handleRefund(w http.ResponseWriter, r *http.Request, claims *oauth.Claims) {
	auth, ok := s.resolveOwnedAuthorization(w, r, claims)
	if !ok {
		return
	}

	// Un cuerpo vacío es válido: es el reembolso total de siempre.
	var req refundRequest
	if r.ContentLength > 0 && !decodeBody(w, r, &req) {
		return
	}
	if req.Amount < 0 {
		writeError(w, http.StatusBadRequest, CodeMalformedRequest,
			"amount no puede ser negativo; omítelo para devolver el total pendiente")
		return
	}

	before := auth.RefundedMicros
	refunded, err := s.core.Refund(r.Context(), auth.ID, req.Amount)
	if err != nil {
		switch {
		case errors.Is(err, coreclient.ErrInvalidState):
			writeError(w, http.StatusConflict, CodeNothingToRefund,
				"la autorización no tiene un cobro que devolver")
		case errors.Is(err, coreclient.ErrInvalid):
			// El core rechaza devolver más de lo que queda. Es 422 y no 400: la
			// petición está bien formada, lo que no cuadra es el monto contra el
			// estado del cobro.
			writeError(w, http.StatusUnprocessableEntity, CodeRefundExceedsCapture,
				"el reembolso supera lo que queda por devolver")
		default:
			coreError(w, err)
		}
		return
	}

	writeJSON(w, http.StatusOK, refundResponse{
		AuthorizationID: encodeID(authorizationPrefix, refunded.ID),
		Status:          statusName(refunded.Status),
		Refunded:        refunded.RefundedMicros - before,
		RefundedTotal:   refunded.RefundedMicros,
		Refundable:      refunded.AmountMicros - refunded.RefundedMicros,
		Currency:        refunded.Currency,
	})
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request, claims *oauth.Claims) {
	auth, ok := s.resolveOwnedAuthorization(w, r, claims)
	if !ok {
		return
	}

	// Verificación opcional del vendedor.
	//
	// El contrato define recipient_mismatch y amount_mismatch pero el GET no
	// recibía con qué comparar. Estos parámetros lo resuelven sin obligar a nadie:
	// si no vienen, la comparación queda del lado del cliente, como antes.
	query := r.URL.Query()
	if raw := query.Get("expected_amount"); raw != "" {
		expected, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, CodeMalformedRequest,
				"expected_amount debe ser un entero en micras")
			return
		}
		if expected != auth.AmountMicros {
			writeError(w, http.StatusUnprocessableEntity, CodeAmountMismatch,
				"el monto autorizado no coincide con el esperado")
			return
		}
	}
	if raw := query.Get("expected_payee_account_id"); raw != "" {
		expected, err := decodeID(accountPrefix, raw)
		if err != nil || expected != auth.PayeeAccountID {
			writeError(w, http.StatusUnprocessableEntity, CodeRecipientMismatch,
				"el receptor de la autorización no coincide con el esperado")
			return
		}
	}

	expires := auth.ExpiresAt
	writeJSON(w, http.StatusOK, authorizationResponse{
		AuthorizationID: encodeID(authorizationPrefix, auth.ID),
		PayerAccountID:  encodeID(accountPrefix, auth.PayerAccountID),
		PayeeAccountID:  encodeID(accountPrefix, auth.PayeeAccountID),
		Amount:          auth.AmountMicros,
		Currency:        auth.Currency,
		Status:          statusName(auth.Status),
		ExpiresAt:       &expires,
		SettledAt:       auth.SettledAt,
	})
}

// ---------------------------------------------------------------- comunes

// resolveOwnedAccount traduce el identificador público y comprueba que la cuenta
// pertenezca a la integración.
//
// Un identificador mal formado, uno inexistente y uno de OTRA integración
// responden todos 404. Distinguirlos permitiría a un socio descubrir qué cuentas
// existen en el banco probando identificadores.
func (s *Server) resolveOwnedAccount(w http.ResponseWriter, r *http.Request, claims *oauth.Claims, public string) (string, bool) {
	if public == "" {
		writeError(w, http.StatusBadRequest, CodeMalformedRequest, "falta el identificador de cuenta")
		return "", false
	}

	id, err := decodeID(accountPrefix, public)
	if err != nil {
		writeError(w, http.StatusNotFound, CodeAccountNotFound, "la cuenta no existe")
		return "", false
	}

	owns, err := s.store.OwnsAccount(r.Context(), claims.Subject, id)
	if err != nil {
		s.log.ErrorContext(r.Context(), "verificar propiedad de cuenta", "error", err)
		writeError(w, http.StatusInternalServerError, CodeServerError, "error interno")
		return "", false
	}
	if !owns {
		writeError(w, http.StatusNotFound, CodeAccountNotFound, "la cuenta no existe")
		return "", false
	}
	return id, true
}

// resolveMandateAccount comprueba que el mandato ampare a esta integración y
// devuelve la cuenta que cubre.
//
// El pagador que mande la plataforma tiene que COINCIDIR con el del mandato. No
// se toma el del mandato en silencio: si la plataforma cree estar pagando desde
// otra cuenta, es un error suyo que conviene que vea, no algo que corregir por
// detrás.
func (s *Server) resolveMandateAccount(w http.ResponseWriter, r *http.Request, claims *oauth.Claims, mandateID, declaredPayer string) (string, bool) {
	mandate, err := s.core.GetMandate(r.Context(), mandateID)
	if err != nil {
		mandateError(w, err)
		return "", false
	}

	// Un mandato otorgado a otra integración no existe para esta.
	if mandate.Grantee != claims.Subject {
		writeError(w, http.StatusNotFound, CodeMandateNotFound, "el mandato no existe")
		return "", false
	}

	if declaredPayer != "" {
		declared, err := decodeID(accountPrefix, declaredPayer)
		if err != nil || declared != mandate.AccountID {
			writeError(w, http.StatusForbidden, CodeMandateAccountScope,
				"el permiso no cubre la cuenta pagadora indicada")
			return "", false
		}
	}

	return mandate.AccountID, true
}

type mandateJSON struct {
	MandateID       string     `json:"mandate_id"`
	AccountID       string     `json:"account_id"`
	Currency        string     `json:"currency"`
	Status          string     `json:"status"`
	MaxPerOperation *int64     `json:"max_per_operation,omitempty"`
	MaxTotal        *int64     `json:"max_total,omitempty"`
	Consumed        int64      `json:"consumed"`
	Remaining       *int64     `json:"remaining,omitempty"`
	ExpiresAt       time.Time  `json:"expires_at"`
	RevokedAt       *time.Time `json:"revoked_at,omitempty"`
}

func toMandateJSON(m *coreclient.Mandate) mandateJSON {
	return mandateJSON{
		MandateID:       m.ID,
		AccountID:       encodeID(accountPrefix, m.AccountID),
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

// resolveOwnedAuthorization carga la autorización y verifica que sus cuentas
// pertenezcan a la integración.
func (s *Server) resolveOwnedAuthorization(w http.ResponseWriter, r *http.Request, claims *oauth.Claims) (*coreclient.Authorization, bool) {
	id, err := decodeID(authorizationPrefix, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, CodeAuthorizationMissing, "la autorización no existe")
		return nil, false
	}

	auth, err := s.core.GetAuthorization(r.Context(), id)
	if err != nil {
		coreError(w, err)
		return nil, false
	}

	// La integración puede operar sobre la autorización si alguna de las dos
	// cuentas es suya.
	//
	// Los dos lados y no solo el pagador: en el modelo B el pagador es la cuenta
	// de un cliente del banco y NO le pertenece a la integración, aunque haya sido
	// ella quien inició el pago. Exigir el pagador dejaría a la plataforma sin
	// poder capturar lo que autorizó.
	//
	// Que baste el receptor no abre nada: `handleAuthorize` exige que el receptor
	// sea siempre una cuenta de la integración, así que el receptor de una
	// autorización identifica a quien la creó.
	for _, account := range []string{auth.PayerAccountID, auth.PayeeAccountID} {
		owns, err := s.store.OwnsAccount(r.Context(), claims.Subject, account)
		if err != nil {
			s.log.ErrorContext(r.Context(), "verificar propiedad de cuenta", "error", err)
			writeError(w, http.StatusInternalServerError, CodeServerError, "error interno")
			return nil, false
		}
		if owns {
			return auth, true
		}
	}

	// Misma respuesta que si no existiera: una autorización de otra integración no
	// puede ni confirmarse ni negarse.
	writeError(w, http.StatusNotFound, CodeAuthorizationMissing, "la autorización no existe")
	return nil, false
}

// statusName traduce el estado del core al vocabulario del contrato.
func statusName(status corev1.AuthorizationStatus) string {
	switch status {
	case corev1.AuthorizationStatus_AUTHORIZATION_STATUS_AUTHORIZED:
		return "authorized"
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

func decodeBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes))
	// Un campo desconocido es un error, no algo que ignorar: casi siempre es una
	// errata en el nombre, y aceptarla en silencio deja al cliente creyendo que
	// mandó un dato que el servidor nunca vio.
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, CodeMalformedRequest, "cuerpo JSON inválido: "+err.Error())
		return false
	}
	return true
}
