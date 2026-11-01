package coreclient

import (
	"context"

	corev1 "github.com/aibank/aibank/clients/go/corev1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// PocketTransferKind es la naturaleza de un movimiento entre una cuenta y sus
// bolsillos. El core lo deja fuera del tope por operación y exige que no salga
// de la familia.
const PocketTransferKind = "pocket_transfer"

// OpenPocket abre un bolsillo de una cuenta: hereda cliente, moneda y producto.
func (c *Client) OpenPocket(ctx context.Context, parentAccountID, code, name string) (*corev1.Account, error) {
	var trailer metadata.MD
	account, err := c.accounts.OpenPocket(ctx, &corev1.OpenPocketRequest{
		ParentAccountId: parentAccountID, Code: code, Name: name,
	}, grpc.Trailer(&trailer))
	if err != nil {
		return nil, translate(err, trailer)
	}
	return account, nil
}

// ClosePocket cierra un bolsillo vacío.
func (c *Client) ClosePocket(ctx context.Context, pocketID string) (*corev1.Account, error) {
	var trailer metadata.MD
	account, err := c.accounts.ClosePocket(ctx, &corev1.ClosePocketRequest{PocketId: pocketID}, grpc.Trailer(&trailer))
	if err != nil {
		return nil, translate(err, trailer)
	}
	return account, nil
}

// MovePocket mueve dinero entre una cuenta y uno de sus bolsillos, en cualquier
// sentido.
func (c *Client) MovePocket(ctx context.Context, idempotencyKey, from, to string, amountMicros int64, currency, description string) (*PostResult, error) {
	return c.Post(ctx, idempotencyKey, PocketTransferKind, []Entry{
		Debit(from, amountMicros, currency),
		Credit(to, amountMicros, currency),
	}, description)
}
