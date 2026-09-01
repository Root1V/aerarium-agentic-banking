package coreclient

import (
	"context"
	"errors"
	"fmt"
	"time"

	corev1 "github.com/aibank/aibank/clients/go/corev1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// Errores propios de las autorizaciones.
//
// Existen aparte de los del ledger porque una API de socio tiene que mapear cada
// uno a un código HTTP distinto, y no puede hacerlo interpretando un mensaje.
var (
	// ErrAuthorizationNotFound: el authorization_id no existe.
	ErrAuthorizationNotFound = errors.New("autorización inexistente")
	// ErrAccountNotFound: la cuenta pagadora o receptora no existe.
	ErrAccountNotFound = errors.New("cuenta inexistente")
	// ErrAccountNotOperative: la cuenta existe pero está congelada o cerrada.
	ErrAccountNotOperative = errors.New("cuenta no operativa")
	// ErrAuthorizationExpired: la vigencia terminó; el dinero se libera, no se captura.
	ErrAuthorizationExpired = errors.New("autorización vencida")
	// ErrInvalidState: la operación no aplica al estado actual.
	ErrInvalidState = errors.New("estado inválido para la operación")
	// ErrCurrencyMismatch: pagador y receptor no comparten moneda.
	ErrCurrencyMismatch = errors.New("monedas distintas")
)

// Authorization es una autorización tal como la reporta el core.
//
// `Status` es el estado OBSERVADO: una retención cuya fecha pasó llega como
// EXPIRED aunque el barrendero todavía no la haya liberado.
type Authorization struct {
	ID             string
	IdempotencyKey string
	PayerAccountID string
	PayeeAccountID string
	AmountMicros   int64
	Currency       string
	Status         corev1.AuthorizationStatus
	RefundedMicros int64
	ExpiresAt      time.Time
	CreatedAt      time.Time
	SettledAt      *time.Time
	ReleasedAt     *time.Time
}

// AuthorizeResult trae la autorización y si vino de una clave ya usada.
type AuthorizeResult struct {
	Authorization *Authorization
	// Replayed indica que la clave ya existía y se devolvió la original. La API
	// de socio lo usa para responder con Idempotent-Replay.
	Replayed bool
}

// Authorize retiene el monto en la cuenta pagadora a favor de la receptora.
func (c *Client) Authorize(ctx context.Context, key, payer, payee string, amountMicros int64, currency string, ttlMinutes int32) (*AuthorizeResult, error) {
	var trailer metadata.MD
	resp, err := c.authorizations.Authorize(ctx, &corev1.AuthorizeRequest{
		IdempotencyKey: key,
		PayerAccountId: payer,
		PayeeAccountId: payee,
		Amount:         &corev1.Money{AmountMicros: amountMicros, Currency: currency},
		TtlMinutes:     ttlMinutes,
	}, grpc.Trailer(&trailer))
	if err != nil {
		return nil, translateAuthorization(err, trailer)
	}
	return &AuthorizeResult{
		Authorization: toAuthorization(resp.GetAuthorization()),
		Replayed:      resp.GetReplayed(),
	}, nil
}

// Capture liquida la retención al receptor.
//
// Idempotente por naturaleza: capturar dos veces devuelve la misma captura, con
// el mismo SettledAt.
func (c *Client) Capture(ctx context.Context, authorizationID string) (*Authorization, error) {
	var trailer metadata.MD
	resp, err := c.authorizations.Capture(ctx,
		&corev1.CaptureRequest{AuthorizationId: authorizationID}, grpc.Trailer(&trailer))
	if err != nil {
		return nil, translateAuthorization(err, trailer)
	}
	return toAuthorization(resp), nil
}

// Refund devuelve dinero ya capturado. `amountMicros` en 0 reembolsa el total
// pendiente.
func (c *Client) Refund(ctx context.Context, authorizationID string, amountMicros int64) (*Authorization, error) {
	var trailer metadata.MD
	resp, err := c.authorizations.Refund(ctx, &corev1.RefundRequest{
		AuthorizationId: authorizationID,
		AmountMicros:    amountMicros,
	}, grpc.Trailer(&trailer))
	if err != nil {
		return nil, translateAuthorization(err, trailer)
	}
	return toAuthorization(resp), nil
}

// GetAuthorization consulta el estado de una autorización.
func (c *Client) GetAuthorization(ctx context.Context, authorizationID string) (*Authorization, error) {
	var trailer metadata.MD
	resp, err := c.authorizations.GetAuthorization(ctx,
		&corev1.GetAuthorizationRequest{AuthorizationId: authorizationID}, grpc.Trailer(&trailer))
	if err != nil {
		return nil, translateAuthorization(err, trailer)
	}
	return toAuthorization(resp), nil
}

// ReleaseExpired libera las retenciones vencidas y devuelve sus identificadores.
func (c *Client) ReleaseExpired(ctx context.Context, limit int32) ([]string, error) {
	var trailer metadata.MD
	resp, err := c.authorizations.ReleaseExpired(ctx,
		&corev1.ReleaseExpiredRequest{Limit: limit}, grpc.Trailer(&trailer))
	if err != nil {
		return nil, translateAuthorization(err, trailer)
	}
	return resp.GetAuthorizationIds(), nil
}

func toAuthorization(pb *corev1.Authorization) *Authorization {
	if pb == nil {
		return nil
	}
	auth := &Authorization{
		ID:             pb.GetId(),
		IdempotencyKey: pb.GetIdempotencyKey(),
		PayerAccountID: pb.GetPayerAccountId(),
		PayeeAccountID: pb.GetPayeeAccountId(),
		AmountMicros:   pb.GetAmount().GetAmountMicros(),
		Currency:       pb.GetAmount().GetCurrency(),
		Status:         pb.GetStatus(),
		RefundedMicros: pb.GetRefundedMicros(),
		ExpiresAt:      pb.GetExpiresAt().AsTime(),
		CreatedAt:      pb.GetCreatedAt().AsTime(),
	}
	if pb.SettledAt != nil {
		t := pb.GetSettledAt().AsTime()
		auth.SettledAt = &t
	}
	if pb.ReleasedAt != nil {
		t := pb.GetReleasedAt().AsTime()
		auth.ReleasedAt = &t
	}
	return auth
}

// translateAuthorization mapea el motivo tipado de los trailers a un error de Go.
//
// Por la misma clave de metadata puede llegar un motivo de autorización o uno de
// posting: cuando el rechazo viene del asiento —fondos insuficientes, por
// ejemplo— el core reusa el motivo del ledger. Se prueban los dos vocabularios
// antes de caer al código gRPC.
func translateAuthorization(err error, trailer metadata.MD) error {
	st, ok := status.FromError(err)
	if !ok {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return fmt.Errorf("%w: %s", sentinelForTrailer(trailer, st.Code()), st.Message())
}

func sentinelForTrailer(trailer metadata.MD, code codes.Code) error {
	values := trailer.Get(reasonMetadataKey)
	if len(values) > 0 {
		if v, found := corev1.AuthorizationErrorReason_value[values[0]]; found {
			if sentinel := authorizationSentinel(corev1.AuthorizationErrorReason(v)); sentinel != nil {
				return sentinel
			}
		}
		if v, found := corev1.PostingErrorReason_value[values[0]]; found {
			if sentinel := sentinelForReason(corev1.PostingErrorReason(v)); sentinel != nil {
				return sentinel
			}
		}
	}
	// Sin motivo reconocible se cae al código gRPC: el core podría ser una
	// versión anterior que todavía no informa este motivo.
	return sentinelForCode(code)
}

func authorizationSentinel(reason corev1.AuthorizationErrorReason) error {
	switch reason {
	case corev1.AuthorizationErrorReason_AUTHORIZATION_ERROR_REASON_INVALID:
		return ErrInvalid
	case corev1.AuthorizationErrorReason_AUTHORIZATION_ERROR_REASON_IDEMPOTENCY_CONFLICT:
		return ErrIdempotencyConflict
	case corev1.AuthorizationErrorReason_AUTHORIZATION_ERROR_REASON_NOT_FOUND:
		return ErrAuthorizationNotFound
	case corev1.AuthorizationErrorReason_AUTHORIZATION_ERROR_REASON_ACCOUNT_NOT_FOUND:
		return ErrAccountNotFound
	case corev1.AuthorizationErrorReason_AUTHORIZATION_ERROR_REASON_ACCOUNT_NOT_OPERATIVE:
		return ErrAccountNotOperative
	case corev1.AuthorizationErrorReason_AUTHORIZATION_ERROR_REASON_EXPIRED:
		return ErrAuthorizationExpired
	case corev1.AuthorizationErrorReason_AUTHORIZATION_ERROR_REASON_INVALID_STATE:
		return ErrInvalidState
	case corev1.AuthorizationErrorReason_AUTHORIZATION_ERROR_REASON_INSUFFICIENT_FUNDS:
		return ErrInsufficientFunds
	case corev1.AuthorizationErrorReason_AUTHORIZATION_ERROR_REASON_CURRENCY_MISMATCH:
		return ErrCurrencyMismatch
	default:
		return nil
	}
}
