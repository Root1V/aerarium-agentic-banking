package mercatus

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/aibank/aibank/clients/go/coreclient"
)

// Códigos de error del contrato. Son parte de la API: un cliente los compara por
// igualdad, así que cambiar uno rompe la integración igual que cambiar una ruta.
const (
	CodeMalformedRequest     = "malformed_request"
	CodeInvalidCredential    = "invalid_credential"
	CodeInsufficientScope    = "insufficient_scope"
	CodeInsufficientFunds    = "insufficient_funds"
	CodeAccountFrozen        = "account_frozen"
	CodeAccountNotFound      = "account_not_found"
	CodeAuthorizationMissing = "authorization_not_found"
	CodeAlreadyCaptured      = "already_captured"
	CodeAlreadyRefunded      = "already_refunded"
	CodeAuthorizationExpired = "authorization_expired"
	CodeIdempotencyReused    = "idempotency_key_reused"
	CodeRecipientMismatch    = "recipient_mismatch"
	CodeAmountMismatch       = "amount_mismatch"
	CodeCurrencyMismatch     = "currency_mismatch"
	CodeSameAccount          = "same_account"
	CodeRateLimited          = "rate_limited"
	CodeNothingToRefund      = "nothing_to_refund"
	CodeServerError          = "server_error"
	CodeServiceUnavailable   = "service_unavailable"
)

type errorBody struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorBody{Error: code, Message: message})
}

// coreError traduce un error del core al par (status, código) del contrato.
//
// La traducción es explícita y no por defecto: cada error del core tiene un
// código HTTP que le dice al cliente qué hacer, y colapsarlos todos en 500
// convertiría "no te alcanza el saldo" en "el banco está roto".
func coreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, coreclient.ErrInsufficientFunds):
		// 402 y no 400: la petición era correcta, lo que falta es dinero.
		writeError(w, http.StatusPaymentRequired, CodeInsufficientFunds,
			"la cuenta pagadora no tiene saldo suficiente")

	case errors.Is(err, coreclient.ErrAccountNotOperative):
		writeError(w, http.StatusForbidden, CodeAccountFrozen,
			"la cuenta está congelada y no admite movimientos")

	case errors.Is(err, coreclient.ErrAccountNotFound):
		writeError(w, http.StatusNotFound, CodeAccountNotFound, "la cuenta no existe")

	case errors.Is(err, coreclient.ErrAuthorizationNotFound):
		writeError(w, http.StatusNotFound, CodeAuthorizationMissing, "la autorización no existe")

	case errors.Is(err, coreclient.ErrAuthorizationExpired):
		writeError(w, http.StatusConflict, CodeAuthorizationExpired,
			"la autorización venció y el dinero se devuelve al pagador")

	case errors.Is(err, coreclient.ErrCurrencyMismatch):
		writeError(w, http.StatusUnprocessableEntity, CodeCurrencyMismatch,
			"pagador y receptor no comparten moneda")

	case errors.Is(err, coreclient.ErrIdempotencyConflict):
		writeError(w, http.StatusConflict, CodeIdempotencyReused,
			"esa Idempotency-Key ya se usó con otro monto o con otras cuentas")

	case errors.Is(err, coreclient.ErrInvalidState):
		// El core distingue los estados; para el contrato basta con decir que la
		// operación no aplica al estado actual.
		writeError(w, http.StatusConflict, CodeAlreadyRefunded,
			"la operación no aplica al estado actual de la autorización")

	case errors.Is(err, coreclient.ErrInvalid):
		writeError(w, http.StatusBadRequest, CodeMalformedRequest, err.Error())

	case errors.Is(err, coreclient.ErrBalanceCapExceeded),
		errors.Is(err, coreclient.ErrTransactionCapExceeded):
		writeError(w, http.StatusUnprocessableEntity, CodeMalformedRequest,
			"el movimiento supera un tope de la cuenta")

	case errors.Is(err, coreclient.ErrUnavailable):
		// 503 y no 500: es transitorio y el cliente PUEDE reintentar con la misma
		// clave de idempotencia sin duplicar nada.
		writeError(w, http.StatusServiceUnavailable, CodeServiceUnavailable,
			"el servicio no está disponible; reintenta con la misma Idempotency-Key")

	default:
		writeError(w, http.StatusInternalServerError, CodeServerError, "error interno")
	}
}
