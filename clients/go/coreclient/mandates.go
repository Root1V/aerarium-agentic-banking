package coreclient

import (
	"context"
	"errors"
	"time"

	corev1 "github.com/aibank/aibank/clients/go/corev1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Errores propios de los mandatos.
//
// Se distinguen de los del ledger porque son problemas distintos con soluciones
// distintas: "no te alcanza el saldo" se arregla fondeando, "no te alcanza el
// permiso" se arregla pidiéndole al titular que amplíe el mandato.
var (
	// ErrMandateNotFound: el mandato no existe o la integración no tiene permiso
	// vivo sobre esa cuenta.
	ErrMandateNotFound = errors.New("mandato inexistente")
	// ErrMandateRevoked: el titular retiró el permiso.
	ErrMandateRevoked = errors.New("mandato revocado")
	// ErrMandateExpired: la vigencia del permiso terminó.
	ErrMandateExpired = errors.New("mandato vencido")
	// ErrMandateLimitExceeded: el pago supera lo que el titular autorizó.
	ErrMandateLimitExceeded = errors.New("tope del mandato superado")
	// ErrMandateAccountNotCovered: el mandato no habilita esa cuenta.
	ErrMandateAccountNotCovered = errors.New("cuenta fuera del mandato")
)

// Mandate es el permiso que un titular otorgó a una integración.
type Mandate struct {
	ID              string
	AccountID       string
	Grantee         string
	GrantedBy       string
	Currency        string
	MaxPerOperation *int64
	MaxTotal        *int64
	// ConsumedMicros lo deriva el core de las autorizaciones, no de un contador.
	ConsumedMicros int64
	Status         corev1.MandateStatus
	ExpiresAt      time.Time
	CreatedAt      time.Time
	RevokedAt      *time.Time
}

// Usable indica si el mandato habilita pagos ahora mismo.
func (m *Mandate) Usable() bool {
	return m.Status == corev1.MandateStatus_MANDATE_STATUS_ACTIVE
}

// RemainingMicros es lo que queda por gastar; nil si no hay tope total.
func (m *Mandate) RemainingMicros() *int64 {
	if m.MaxTotal == nil {
		return nil
	}
	remaining := *m.MaxTotal - m.ConsumedMicros
	if remaining < 0 {
		remaining = 0
	}
	return &remaining
}

// GrantMandate registra el permiso que un titular otorgó tras autenticarse.
//
// `consentReference` es obligatorio: es la evidencia de que esa persona aprobó
// esto, y sin ella el mandato no prueba nada.
func (c *Client) GrantMandate(ctx context.Context, accountID, grantee, grantedBy, consentReference string, maxPerOperation, maxTotal *int64, expiresAt time.Time) (*Mandate, error) {
	var trailer metadata.MD
	resp, err := c.mandates.GrantMandate(ctx, &corev1.GrantMandateRequest{
		AccountId:             accountID,
		Grantee:               grantee,
		GrantedBy:             grantedBy,
		ConsentReference:      consentReference,
		MaxPerOperationMicros: maxPerOperation,
		MaxTotalMicros:        maxTotal,
		ExpiresAt:             timestamppb.New(expiresAt),
	}, grpc.Trailer(&trailer))
	if err != nil {
		return nil, translateMandate(err, trailer)
	}
	return toMandate(resp), nil
}

// RevokeMandate retira el permiso. Idempotente.
func (c *Client) RevokeMandate(ctx context.Context, mandateID, revokedBy string) (*Mandate, error) {
	var trailer metadata.MD
	resp, err := c.mandates.RevokeMandate(ctx, &corev1.RevokeMandateRequest{
		MandateId: mandateID, RevokedBy: revokedBy,
	}, grpc.Trailer(&trailer))
	if err != nil {
		return nil, translateMandate(err, trailer)
	}
	return toMandate(resp), nil
}

func (c *Client) GetMandate(ctx context.Context, mandateID string) (*Mandate, error) {
	var trailer metadata.MD
	resp, err := c.mandates.GetMandate(ctx,
		&corev1.GetMandateRequest{MandateId: mandateID}, grpc.Trailer(&trailer))
	if err != nil {
		return nil, translateMandate(err, trailer)
	}
	return toMandate(resp), nil
}

// FindActiveMandate busca el permiso vivo de una integración sobre una cuenta.
//
// Es la comprobación que reemplaza a "¿esta cuenta es tuya?" en el modelo B.
// Devuelve ErrMandateNotFound si no hay ninguno.
func (c *Client) FindActiveMandate(ctx context.Context, accountID, grantee string) (*Mandate, error) {
	var trailer metadata.MD
	resp, err := c.mandates.FindActiveMandate(ctx, &corev1.FindActiveMandateRequest{
		AccountId: accountID, Grantee: grantee,
	}, grpc.Trailer(&trailer))
	if err != nil {
		return nil, translateMandate(err, trailer)
	}
	return toMandate(resp), nil
}

// ListMandates devuelve los mandatos de un titular, para su app.
func (c *Client) ListMandates(ctx context.Context, grantedBy string) ([]*Mandate, error) {
	var trailer metadata.MD
	resp, err := c.mandates.ListMandates(ctx,
		&corev1.ListMandatesRequest{GrantedBy: grantedBy}, grpc.Trailer(&trailer))
	if err != nil {
		return nil, translateMandate(err, trailer)
	}
	out := make([]*Mandate, 0, len(resp.GetMandates()))
	for _, m := range resp.GetMandates() {
		out = append(out, toMandate(m))
	}
	return out, nil
}

// AuthorizeUnderMandate inicia un pago amparado por un mandato.
//
// El core comprueba el permiso y retiene el dinero en la misma transacción: no
// hay ventana entre "tenías permiso" y "se retuvo".
func (c *Client) AuthorizeUnderMandate(ctx context.Context, mandateID, key, payer, payee string, amountMicros int64, currency string) (*AuthorizeResult, *Mandate, error) {
	var trailer metadata.MD
	resp, err := c.mandates.AuthorizeUnderMandate(ctx, &corev1.AuthorizeUnderMandateRequest{
		MandateId: mandateID,
		Authorization: &corev1.AuthorizeRequest{
			IdempotencyKey: key,
			PayerAccountId: payer,
			PayeeAccountId: payee,
			Amount:         &corev1.Money{AmountMicros: amountMicros, Currency: currency},
		},
	}, grpc.Trailer(&trailer))
	if err != nil {
		return nil, nil, translateMandate(err, trailer)
	}
	return &AuthorizeResult{
		Authorization: toAuthorization(resp.GetAuthorization()),
		Replayed:      resp.GetReplayed(),
	}, toMandate(resp.GetMandate()), nil
}

func toMandate(pb *corev1.Mandate) *Mandate {
	if pb == nil {
		return nil
	}
	m := &Mandate{
		ID:              pb.GetId(),
		AccountID:       pb.GetAccountId(),
		Grantee:         pb.GetGrantee(),
		GrantedBy:       pb.GetGrantedBy(),
		Currency:        pb.GetCurrency(),
		MaxPerOperation: pb.MaxPerOperationMicros,
		MaxTotal:        pb.MaxTotalMicros,
		ConsumedMicros:  pb.GetConsumedMicros(),
		Status:          pb.GetStatus(),
		ExpiresAt:       pb.GetExpiresAt().AsTime(),
		CreatedAt:       pb.GetCreatedAt().AsTime(),
	}
	if pb.RevokedAt != nil {
		t := pb.GetRevokedAt().AsTime()
		m.RevokedAt = &t
	}
	return m
}

// translateMandate mapea el motivo tipado a un error de Go.
//
// Por la misma clave de metadata puede llegar un motivo de mandato, uno de
// autorización o uno de posting: cuando el rechazo viene del dinero y no del
// permiso, el core reusa el motivo de esa capa. Se prueban los tres.
func translateMandate(err error, trailer metadata.MD) error {
	values := trailer.Get(reasonMetadataKey)
	if len(values) > 0 {
		if v, found := corev1.MandateErrorReason_value[values[0]]; found {
			if sentinel := mandateSentinel(corev1.MandateErrorReason(v)); sentinel != nil {
				return wrap(sentinel, err)
			}
		}
	}
	return translateAuthorization(err, trailer)
}

func mandateSentinel(reason corev1.MandateErrorReason) error {
	switch reason {
	case corev1.MandateErrorReason_MANDATE_ERROR_REASON_INVALID:
		return ErrInvalid
	case corev1.MandateErrorReason_MANDATE_ERROR_REASON_NOT_FOUND:
		return ErrMandateNotFound
	case corev1.MandateErrorReason_MANDATE_ERROR_REASON_REVOKED:
		return ErrMandateRevoked
	case corev1.MandateErrorReason_MANDATE_ERROR_REASON_EXPIRED:
		return ErrMandateExpired
	case corev1.MandateErrorReason_MANDATE_ERROR_REASON_LIMIT_EXCEEDED:
		return ErrMandateLimitExceeded
	case corev1.MandateErrorReason_MANDATE_ERROR_REASON_ACCOUNT_NOT_COVERED:
		return ErrMandateAccountNotCovered
	default:
		return nil
	}
}
