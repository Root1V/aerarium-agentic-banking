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
	CodeRefundExceedsCapture = "refund_exceeds_capture"
	CodeServerError          = "server_error"
	CodeServiceUnavailable   = "service_unavailable"

	// Modelo B: permiso delegado por el titular de una cuenta.
	CodeConsentNotFound      = "consent_request_not_found"
	CodeConsentResolved      = "consent_request_already_resolved"
	CodeMandateNotFound      = "mandate_not_found"
	CodeMandateRevoked       = "mandate_revoked"
	CodeMandateExpired       = "mandate_expired"
	CodeMandateLimitExceeded = "mandate_limit_exceeded"
	CodeMandateAccountScope  = "mandate_account_not_covered"
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

// mandateError traduce los rechazos del modelo B.
//
// Se separa de coreError porque "no te alcanza el permiso" y "no te alcanza el
// saldo" son problemas distintos con soluciones distintas: uno se arregla
// pidiéndole al titular que amplíe el mandato, el otro fondeando la cuenta.
// Colapsarlos dejaría al cliente sin saber a quién recurrir.
func mandateError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, coreclient.ErrMandateNotFound):
		writeError(w, http.StatusNotFound, CodeMandateNotFound,
			"no hay un permiso vigente sobre esa cuenta")

	case errors.Is(err, coreclient.ErrMandateRevoked):
		// 403 y no 404: el permiso existió y el titular lo retiró. Decirlo permite
		// a la plataforma pedir uno nuevo en vez de buscar un error propio.
		writeError(w, http.StatusForbidden, CodeMandateRevoked,
			"el titular retiró el permiso")

	case errors.Is(err, coreclient.ErrMandateExpired):
		writeError(w, http.StatusForbidden, CodeMandateExpired,
			"el permiso venció; hay que pedir uno nuevo")

	case errors.Is(err, coreclient.ErrMandateLimitExceeded):
		// 422 y no 402: hay saldo, lo que falta es autorización.
		writeError(w, http.StatusUnprocessableEntity, CodeMandateLimitExceeded,
			"el pago supera lo que el titular autorizó")

	case errors.Is(err, coreclient.ErrMandateAccountNotCovered):
		writeError(w, http.StatusForbidden, CodeMandateAccountScope,
			"el permiso no cubre esa cuenta")

	default:
		coreError(w, err)
	}
}
