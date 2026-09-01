// Package coreclient es el cliente Go del core bancario.
//
// Su valor añadido sobre el código generado es traducir los rechazos del core a
// errores tipados de Go: un adaptador debe poder distinguir "fondos insuficientes"
// (definitivo — no reintentar, informar al cliente) de "core no disponible"
// (transitorio — reintentar con backoff). Confundirlos es cómo se duplican pagos.
package coreclient

import (
	"context"
	"fmt"
	"time"

	corev1 "github.com/aibank/aibank/clients/go/corev1"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

// Client habla con el core bancario.
type Client struct {
	conn           *grpc.ClientConn
	ledger         corev1.LedgerServiceClient
	accounts       corev1.AccountServiceClient
	products       corev1.ProductServiceClient
	authorizations corev1.AuthorizationServiceClient
}

// Dial abre una conexión con el core.
//
// Sin TLS: el core solo escucha en la red interna. La terminación TLS y la
// autenticación entre servicios se resuelven en la malla, no aquí.
func Dial(_ context.Context, target string) (*Client, error) {
	// El interceptor propaga el contexto de traza al core por la metadata gRPC.
	// Sin él, la traza se corta en el borde del proceso y seguir un pago de
	// extremo a extremo deja de ser posible.
	conn, err := grpc.NewClient(target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithStatsHandler(otelgrpc.NewClientHandler()),
	)
	if err != nil {
		return nil, fmt.Errorf("conectar al core: %w", err)
	}
	return &Client{
		conn:           conn,
		ledger:         corev1.NewLedgerServiceClient(conn),
		accounts:       corev1.NewAccountServiceClient(conn),
		products:       corev1.NewProductServiceClient(conn),
		authorizations: corev1.NewAuthorizationServiceClient(conn),
	}, nil
}

func (c *Client) Close() error { return c.conn.Close() }

// ---------------------------------------------------------------- ledger

// Entry es un asiento contable. El monto es siempre positivo y va en micras
// (millonésimas, 10^-6); el signo lo aporta la dirección.
type Entry struct {
	AccountID    string
	Direction    corev1.Direction
	AmountMicros int64
	Currency     string
}

// Debit construye un asiento al debe.
func Debit(accountID string, amountMicros int64, currency string) Entry {
	return Entry{accountID, corev1.Direction_DIRECTION_DEBIT, amountMicros, currency}
}

// Credit construye un asiento al haber.
func Credit(accountID string, amountMicros int64, currency string) Entry {
	return Entry{accountID, corev1.Direction_DIRECTION_CREDIT, amountMicros, currency}
}

// PostResult describe el efecto de un movimiento.
type PostResult struct {
	TransactionID string
	PostedAt      time.Time
	// Replayed indica que la clave ya existía: el core devolvió la transacción
	// original sin duplicar el efecto. No es un error: es la idempotencia funcionando.
	Replayed bool
}

// Post asienta un movimiento en el ledger.
//
// idempotencyKey debe ser estable para la MISMA operación de negocio: si un riel
// reenvía el mismo webhook, reusar la clave evita el doble abono.
func (c *Client) Post(ctx context.Context, idempotencyKey, kind string, entries []Entry, description string) (*PostResult, error) {
	pbEntries := make([]*corev1.Entry, 0, len(entries))
	for _, e := range entries {
		pbEntries = append(pbEntries, &corev1.Entry{
			AccountId: e.AccountID,
			Direction: e.Direction,
			Amount:    &corev1.Money{AmountMicros: e.AmountMicros, Currency: e.Currency},
		})
	}

	var trailer metadata.MD
	resp, err := c.ledger.Post(ctx, &corev1.PostRequest{
		IdempotencyKey: idempotencyKey,
		Kind:           kind,
		Entries:        pbEntries,
		Description:    description,
	}, grpc.Trailer(&trailer))
	if err != nil {
		return nil, translate(err, trailer)
	}

	return &PostResult{
		TransactionID: resp.TransactionId,
		PostedAt:      resp.PostedAt.AsTime(),
		Replayed:      resp.Replayed,
	}, nil
}

// Balance es el saldo de una cuenta junto a su verificación contra el ledger.
type Balance struct {
	AmountMicros int64
	Currency     string
	EntryCount   int64
	// ProjectedMicros recalcula el saldo desde los asientos. Si difiere de
	// AmountMicros hay una inconsistencia contable que debe escalarse.
	ProjectedMicros int64
}

// Consistent indica si el saldo materializado coincide con la proyección del ledger.
func (b Balance) Consistent() bool { return b.AmountMicros == b.ProjectedMicros }

func (c *Client) GetBalance(ctx context.Context, accountID string) (*Balance, error) {
	var trailer metadata.MD
	resp, err := c.ledger.GetBalance(ctx,
		&corev1.GetBalanceRequest{AccountId: accountID}, grpc.Trailer(&trailer))
	if err != nil {
		return nil, translate(err, trailer)
	}
	return &Balance{
		AmountMicros:    resp.Balance.AmountMicros,
		Currency:        resp.Balance.Currency,
		EntryCount:      resp.EntryCount,
		ProjectedMicros: resp.ProjectedBalance.AmountMicros,
	}, nil
}

// Movement es un movimiento del extracto.
type Movement struct {
	Cursor        string
	TransactionID string
	// Direction indica si el movimiento suma o resta en la cuenta consultada.
	Direction    corev1.Direction
	AmountMicros int64
	Currency     string
	Kind         string
	Description  string
	PostedAt     time.Time
}

// Statement es una página del extracto.
type Statement struct {
	Movements []Movement
	// NextCursor vacío significa que no hay más páginas.
	NextCursor string
}

// ListMovements devuelve el extracto de una cuenta, del más reciente al más antiguo.
func (c *Client) ListMovements(ctx context.Context, accountID string, limit int32, cursor string) (*Statement, error) {
	var trailer metadata.MD
	resp, err := c.ledger.ListEntries(ctx, &corev1.ListEntriesRequest{
		AccountId: accountID, Limit: limit, Cursor: cursor,
	}, grpc.Trailer(&trailer))
	if err != nil {
		return nil, translate(err, trailer)
	}

	movements := make([]Movement, 0, len(resp.Entries))
	for _, e := range resp.Entries {
		movements = append(movements, Movement{
			Cursor:        e.Cursor,
			TransactionID: e.TransactionId,
			Direction:     e.Direction,
			AmountMicros:  e.Amount.AmountMicros,
			Currency:      e.Amount.Currency,
			Kind:          e.Kind,
			Description:   e.Description,
			PostedAt:      e.PostedAt.AsTime(),
		})
	}
	return &Statement{Movements: movements, NextCursor: resp.NextCursor}, nil
}

// ---------------------------------------------------------------- cuentas

func (c *Client) OpenCustomerAccount(ctx context.Context, code, name, customerID, productCode string) (*corev1.Account, error) {
	var trailer metadata.MD
	account, err := c.accounts.OpenCustomerAccount(ctx, &corev1.OpenCustomerAccountRequest{
		Code: code, Name: name, CustomerId: customerID, ProductCode: productCode,
	}, grpc.Trailer(&trailer))
	if err != nil {
		return nil, translate(err, trailer)
	}
	return account, nil
}

func (c *Client) CreateInternalAccount(ctx context.Context, code, name string, accountType corev1.AccountType, currency string) (*corev1.Account, error) {
	var trailer metadata.MD
	account, err := c.accounts.CreateInternalAccount(ctx, &corev1.CreateInternalAccountRequest{
		Code: code, Name: name, Type: accountType, Currency: currency,
	}, grpc.Trailer(&trailer))
	if err != nil {
		return nil, translate(err, trailer)
	}
	return account, nil
}

func (c *Client) GetAccount(ctx context.Context, code string) (*corev1.Account, error) {
	var trailer metadata.MD
	account, err := c.accounts.GetAccount(ctx,
		&corev1.GetAccountRequest{Code: code}, grpc.Trailer(&trailer))
	if err != nil {
		return nil, translate(err, trailer)
	}
	return account, nil
}

// GetAccountByID busca una cuenta por su identificador interno.
func (c *Client) GetAccountByID(ctx context.Context, id string) (*corev1.Account, error) {
	var trailer metadata.MD
	account, err := c.accounts.GetAccountById(ctx,
		&corev1.GetAccountByIdRequest{Id: id}, grpc.Trailer(&trailer))
	if err != nil {
		return nil, translate(err, trailer)
	}
	return account, nil
}

// ---------------------------------------------------------------- productos

// NewProduct describe un producto del catálogo. Los topes nil significan "sin tope".
type NewProduct struct {
	Code                 string
	Name                 string
	Currency             string
	AllowsOverdraft      bool
	MaxBalanceMicros     *int64
	MaxTransactionMicros *int64
}

func (c *Client) CreateProduct(ctx context.Context, p NewProduct) (*corev1.Product, error) {
	var trailer metadata.MD
	product, err := c.products.CreateProduct(ctx, &corev1.CreateProductRequest{
		Code:                 p.Code,
		Name:                 p.Name,
		Currency:             p.Currency,
		AllowsOverdraft:      p.AllowsOverdraft,
		MaxBalanceMicros:     p.MaxBalanceMicros,
		MaxTransactionMicros: p.MaxTransactionMicros,
	}, grpc.Trailer(&trailer))
	if err != nil {
		return nil, translate(err, trailer)
	}
	return product, nil
}

func (c *Client) GetProduct(ctx context.Context, code string) (*corev1.Product, error) {
	var trailer metadata.MD
	product, err := c.products.GetProduct(ctx,
		&corev1.GetProductRequest{Code: code}, grpc.Trailer(&trailer))
	if err != nil {
		return nil, translate(err, trailer)
	}
	return product, nil
}
