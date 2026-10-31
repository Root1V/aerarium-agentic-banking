package coreclient

import (
	"context"
	"time"

	corev1 "github.com/aibank/aibank/clients/go/corev1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// NewCreditLine son las condiciones con que se abre una línea de crédito.
type NewCreditLine struct {
	CustomerID    string
	Name          string
	Currency      string
	LimitMicros   int64
	AnnualRateBps int32
	CycleDay      int32
	PaymentDays   int32
	// DecisionRef es la referencia al expediente de la decisión de crédito.
	DecisionRef string
}

// OpenCreditLine abre la línea que otorgó una decisión de crédito.
// ErrAlreadyExists si el cliente ya tiene una línea viva.
func (c *Client) OpenCreditLine(ctx context.Context, line NewCreditLine) (*corev1.CreditLine, error) {
	var trailer metadata.MD
	resp, err := c.credit.OpenCreditLine(ctx, &corev1.OpenCreditLineRequest{
		CustomerId: line.CustomerID, Name: line.Name, Currency: line.Currency,
		LimitMicros: line.LimitMicros, AnnualRateBps: line.AnnualRateBps,
		CycleDay: line.CycleDay, PaymentDays: line.PaymentDays, DecisionRef: line.DecisionRef,
	}, grpc.Trailer(&trailer))
	if err != nil {
		return nil, translate(err, trailer)
	}
	return resp, nil
}

// GetCustomerCreditLine devuelve la línea viva del cliente. ErrNotFound si no tiene.
func (c *Client) GetCustomerCreditLine(ctx context.Context, customerID string) (*corev1.CreditLine, error) {
	var trailer metadata.MD
	resp, err := c.credit.GetCustomerCreditLine(ctx, &corev1.GetCustomerCreditLineRequest{CustomerId: customerID},
		grpc.Trailer(&trailer))
	if err != nil {
		return nil, translate(err, trailer)
	}
	return resp, nil
}

// CloseCycle corta el estado de cuenta de la línea. cutAt nil: ahora.
func (c *Client) CloseCycle(ctx context.Context, lineID string, cutAt *time.Time) (*corev1.CreditStatement, error) {
	req := &corev1.CloseCycleRequest{LineId: lineID}
	if cutAt != nil {
		req.CutAt = timestamppb.New(*cutAt)
	}
	var trailer metadata.MD
	resp, err := c.credit.CloseCycle(ctx, req, grpc.Trailer(&trailer))
	if err != nil {
		return nil, translate(err, trailer)
	}
	return resp, nil
}

// ListStatements devuelve los estados de cuenta, del más reciente al más antiguo.
func (c *Client) ListStatements(ctx context.Context, lineID string, limit int32) ([]*corev1.CreditStatement, error) {
	var trailer metadata.MD
	resp, err := c.credit.ListStatements(ctx, &corev1.ListStatementsRequest{LineId: lineID, Limit: limit}, grpc.Trailer(&trailer))
	if err != nil {
		return nil, translate(err, trailer)
	}
	return resp.Statements, nil
}

// ReviewCreditLine revisa la mora de la línea a una fecha. asOf nil: ahora.
func (c *Client) ReviewCreditLine(ctx context.Context, lineID string, asOf *time.Time) (*corev1.CreditLine, error) {
	req := &corev1.ReviewCreditLineRequest{LineId: lineID}
	if asOf != nil {
		req.AsOf = timestamppb.New(*asOf)
	}
	var trailer metadata.MD
	resp, err := c.credit.ReviewCreditLine(ctx, req, grpc.Trailer(&trailer))
	if err != nil {
		return nil, translate(err, trailer)
	}
	return resp, nil
}
