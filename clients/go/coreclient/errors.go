package coreclient

import (
	"errors"
	"fmt"

	corev1 "github.com/aibank/aibank/clients/go/corev1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// reasonMetadataKey debe coincidir con REASON_METADATA_KEY del core.
const reasonMetadataKey = "x-aibank-reason"

// Errores del core. Todos son definitivos salvo ErrUnavailable.
var (
	// ErrInvalid: la solicitud viola una regla del ledger (desbalance, monto <= 0).
	ErrInvalid = errors.New("solicitud inválida")
	// ErrIdempotencyConflict: la clave ya se usó con un payload distinto.
	// Señala un bug del llamador, no una condición de negocio.
	ErrIdempotencyConflict = errors.New("conflicto de idempotencia")
	// ErrInsufficientFunds: la cuenta quedaría en negativo sin permitir sobregiro.
	ErrInsufficientFunds = errors.New("fondos insuficientes")
	// ErrBalanceCapExceeded: el saldo superaría el tope regulatorio del producto.
	ErrBalanceCapExceeded = errors.New("tope de saldo excedido")
	// ErrTransactionCapExceeded: el monto supera el tope por operación del producto.
	ErrTransactionCapExceeded = errors.New("tope por operación excedido")
	// ErrNotFound: la cuenta o el producto no existe.
	ErrNotFound = errors.New("no encontrado")
	// ErrUnavailable: fallo de infraestructura. ESTE SÍ es reintentable.
	ErrUnavailable = errors.New("core no disponible")
)

// Retryable indica si tiene sentido reintentar la operación.
//
// Reintentar es seguro incluso en escrituras porque el core es idempotente por
// clave: reenviar la MISMA idempotency_key no duplica el efecto.
func Retryable(err error) bool { return errors.Is(err, ErrUnavailable) }

// translate convierte un error gRPC en un error tipado, leyendo el motivo exacto
// de los trailers y cayendo al código gRPC cuando el core no lo informó.
func translate(err error, trailer metadata.MD) error {
	st, ok := status.FromError(err)
	if !ok {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}

	sentinel := sentinelForReason(reasonFrom(trailer))
	if sentinel == nil {
		sentinel = sentinelForCode(st.Code())
	}
	return fmt.Errorf("%w: %s", sentinel, st.Message())
}

func reasonFrom(trailer metadata.MD) corev1.PostingErrorReason {
	values := trailer.Get(reasonMetadataKey)
	if len(values) == 0 {
		return corev1.PostingErrorReason_POSTING_ERROR_REASON_UNSPECIFIED
	}
	if v, found := corev1.PostingErrorReason_value[values[0]]; found {
		return corev1.PostingErrorReason(v)
	}
	return corev1.PostingErrorReason_POSTING_ERROR_REASON_UNSPECIFIED
}

func sentinelForReason(reason corev1.PostingErrorReason) error {
	switch reason {
	case corev1.PostingErrorReason_POSTING_ERROR_REASON_INVALID:
		return ErrInvalid
	case corev1.PostingErrorReason_POSTING_ERROR_REASON_IDEMPOTENCY_CONFLICT:
		return ErrIdempotencyConflict
	case corev1.PostingErrorReason_POSTING_ERROR_REASON_INSUFFICIENT_FUNDS:
		return ErrInsufficientFunds
	case corev1.PostingErrorReason_POSTING_ERROR_REASON_BALANCE_CAP_EXCEEDED:
		return ErrBalanceCapExceeded
	case corev1.PostingErrorReason_POSTING_ERROR_REASON_TRANSACTION_CAP_EXCEEDED:
		return ErrTransactionCapExceeded
	default:
		return nil
	}
}

func sentinelForCode(code codes.Code) error {
	switch code {
	case codes.NotFound:
		return ErrNotFound
	case codes.InvalidArgument:
		return ErrInvalid
	case codes.Aborted:
		return ErrIdempotencyConflict
	case codes.FailedPrecondition:
		return ErrInvalid
	default:
		// Unavailable, DeadlineExceeded, Internal y demás: fallo de infraestructura.
		return ErrUnavailable
	}
}
