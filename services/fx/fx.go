// Package fx cambia dinero entre las cuentas propias de un cliente, soles y
// dólares (doc 15, F3-09).
//
// En el Perú los bancos fijan su propio tipo de cambio, y la diferencia con el de
// referencia varía mucho de un banco a otro sin que nadie se la diga al cliente.
// Aquí cada cotización trae el tipo de referencia, el que se aplica y lo que
// cuesta el cambio en dinero, y se congela unos segundos: lo que se ve es lo que
// se ejecuta.
//
// Contablemente, el ledger exige que cada operación cuadre por moneda. Un cambio
// pasa por una posición de cambio por moneda: los dólares que salen de la cuenta
// entran a FX-POSITION-USD, y los soles salen de FX-POSITION-PEN hacia la cuenta
// en soles. La posición del banco en cada moneda queda a la vista.
package fx

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/aibank/aibank/clients/go/coreclient"
	corev1 "github.com/aibank/aibank/clients/go/corev1"
	"github.com/google/uuid"
)

//go:embed schema.sql
var schema string

// QuoteTTL: cuánto vale una cotización. Suficiente para leerla y confirmar.
const QuoteTTL = 30 * time.Second

// PositionPrefix: la cuenta de posición de cambio de cada moneda.
const PositionPrefix = "FX-POSITION-"

// Kind es la naturaleza del asiento de un cambio.
const Kind = "fx_conversion"

var (
	ErrNotFound     = errors.New("esa cotización no existe")
	ErrExpired      = errors.New("la cotización venció: pide otra")
	ErrUsed         = errors.New("esa cotización ya se usó")
	ErrSameCurrency = errors.New("las dos cuentas son de la misma moneda")
	ErrPair         = errors.New("solo se cambia entre soles y dólares")
	ErrNotOwned     = errors.New("las dos cuentas tienen que ser tuyas")
)

// Rates da el tipo de cambio de referencia: soles por dólar, en millonésimas.
type Rates interface {
	ReferencePENPerUSD(ctx context.Context) (int64, error)
}

// FixedRate es una referencia fija, para el sandbox. En producción, la del día de
// la SBS o del BCRP.
type FixedRate int64

func (r FixedRate) ReferencePENPerUSD(context.Context) (int64, error) { return int64(r), nil }

// Quote es una cotización.
type Quote struct {
	ID            string
	FromAccountID string
	ToAccountID   string
	FromCurrency  string
	ToCurrency    string
	FromMicros    int64
	ToMicros      int64
	// ReferenceRate y AppliedRate: soles por dólar, en millonésimas.
	ReferenceRate int64
	AppliedRate   int64
	// CostMicros: lo que se deja de recibir frente a la referencia, en la moneda
	// que se recibe.
	CostMicros int64
	ExpiresAt  time.Time
}

type Service struct {
	db   *sql.DB
	core *coreclient.Client
	// spreadBps: el margen del banco sobre la referencia, en puntos básicos, a
	// cada lado. 50 es 0,5 %.
	spreadBps int64
	rates     Rates
	now       func() time.Time
}

func NewService(db *sql.DB, core *coreclient.Client, rates Rates, spreadBps int64) *Service {
	return &Service{db: db, core: core, rates: rates, spreadBps: spreadBps, now: time.Now}
}

// SetClock cambia el reloj, para probar que una cotización vence.
func (s *Service) SetClock(now func() time.Time) { s.now = now }

func (s *Service) Migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, schema)
	return err
}

// EnsurePositions crea las cuentas de posición de cambio si faltan.
func (s *Service) EnsurePositions(ctx context.Context) error {
	for _, currency := range []string{"USD", "PEN"} {
		_, err := s.core.CreateInternalAccount(ctx, PositionPrefix+currency, "Posición de cambio "+currency,
			corev1.AccountType_ACCOUNT_TYPE_ASSET, currency)
		if err != nil && !errors.Is(err, coreclient.ErrAlreadyExists) {
			return err
		}
	}
	return nil
}

// Quote cotiza cambiar `fromMicros` de una cuenta del cliente a otra suya de la
// otra moneda.
func (s *Service) Quote(ctx context.Context, customerID, fromID, toID string, fromMicros int64) (*Quote, error) {
	from, to, err := s.ownedPair(ctx, customerID, fromID, toID)
	if err != nil {
		return nil, err
	}
	reference, err := s.rates.ReferencePENPerUSD(ctx)
	if err != nil {
		return nil, fmt.Errorf("tipo de cambio de referencia: %w", err)
	}
	q := Quote{ID: uuid.NewString(), FromAccountID: fromID, ToAccountID: toID,
		FromCurrency: from.Currency, ToCurrency: to.Currency, FromMicros: fromMicros,
		ReferenceRate: reference, ExpiresAt: s.now().Add(QuoteTTL)}

	// Quien vende dólares recibe menos soles por cada uno (compra del banco); quien
	// compra dólares paga más soles por cada uno (venta del banco).
	var atReference int64
	if from.Currency == "USD" {
		q.AppliedRate = reference * (10_000 - s.spreadBps) / 10_000
		q.ToMicros = mulDiv(fromMicros, q.AppliedRate, 1_000_000)
		atReference = mulDiv(fromMicros, reference, 1_000_000)
	} else {
		q.AppliedRate = reference * (10_000 + s.spreadBps) / 10_000
		q.ToMicros = mulDiv(fromMicros, 1_000_000, q.AppliedRate)
		atReference = mulDiv(fromMicros, 1_000_000, reference)
	}
	// Lo que se recibe se trunca al céntimo. Esa fracción también es costo, y
	// por eso el costo se calcula después, sobre lo que de verdad se recibe.
	q.ToMicros -= q.ToMicros % 10_000
	if q.ToMicros <= 0 {
		return nil, errors.New("el monto es demasiado chico para cambiar")
	}
	q.CostMicros = atReference - q.ToMicros

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO fx.quotes (id, customer_id, from_account_id, to_account_id, from_currency, to_currency,
			from_micros, to_micros, reference_rate, applied_rate, cost_micros, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		q.ID, customerID, fromID, toID, q.FromCurrency, q.ToCurrency, q.FromMicros, q.ToMicros,
		q.ReferenceRate, q.AppliedRate, q.CostMicros, q.ExpiresAt)
	if err != nil {
		return nil, err
	}
	return &q, nil
}

// Convert ejecuta una cotización vigente del cliente. Una cotización se usa una
// sola vez; un reintento con la misma clave devuelve la misma operación.
func (s *Service) Convert(ctx context.Context, customerID, quoteID string) (*Quote, *coreclient.PostResult, error) {
	if _, err := uuid.Parse(quoteID); err != nil {
		return nil, nil, ErrNotFound
	}
	var q Quote
	var txID sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT id, from_account_id, to_account_id, from_currency, to_currency, from_micros, to_micros,
		       reference_rate, applied_rate, cost_micros, expires_at, transaction_id
		  FROM fx.quotes WHERE id = $1 AND customer_id = $2`, quoteID, customerID).
		Scan(&q.ID, &q.FromAccountID, &q.ToAccountID, &q.FromCurrency, &q.ToCurrency, &q.FromMicros,
			&q.ToMicros, &q.ReferenceRate, &q.AppliedRate, &q.CostMicros, &q.ExpiresAt, &txID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	if !txID.Valid && s.now().After(q.ExpiresAt) {
		return nil, nil, ErrExpired
	}
	fromPos, err := s.core.GetAccount(ctx, PositionPrefix+q.FromCurrency)
	if err != nil {
		return nil, nil, err
	}
	toPos, err := s.core.GetAccount(ctx, PositionPrefix+q.ToCurrency)
	if err != nil {
		return nil, nil, err
	}
	// La clave es la cotización: ejecutarla dos veces es la misma operación.
	result, err := s.core.Post(ctx, "fx:"+q.ID, Kind, []coreclient.Entry{
		coreclient.Debit(q.FromAccountID, q.FromMicros, q.FromCurrency),
		coreclient.Credit(fromPos.Id, q.FromMicros, q.FromCurrency),
		coreclient.Debit(toPos.Id, q.ToMicros, q.ToCurrency),
		coreclient.Credit(q.ToAccountID, q.ToMicros, q.ToCurrency),
	}, fmt.Sprintf("Cambio a %s · %s por dólar", q.ToCurrency, rateText(q.AppliedRate)))
	if err != nil {
		return nil, nil, err
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE fx.quotes SET transaction_id = $2 WHERE id = $1 AND transaction_id IS NULL`, q.ID, result.TransactionID); err != nil {
		return nil, nil, err
	}
	return &q, result, nil
}

func (s *Service) ownedPair(ctx context.Context, customerID, fromID, toID string) (*corev1.Account, *corev1.Account, error) {
	from, err := s.core.GetAccountByID(ctx, fromID)
	if err != nil {
		return nil, nil, ErrNotOwned
	}
	to, err := s.core.GetAccountByID(ctx, toID)
	if err != nil {
		return nil, nil, ErrNotOwned
	}
	if from.GetOwnerId() != customerID || to.GetOwnerId() != customerID ||
		from.GetParentAccountId() != "" || to.GetParentAccountId() != "" {
		return nil, nil, ErrNotOwned
	}
	if from.Currency == to.Currency {
		return nil, nil, ErrSameCurrency
	}
	pair := from.Currency + to.Currency
	if pair != "USDPEN" && pair != "PENUSD" {
		return nil, nil, ErrPair
	}
	return from, to, nil
}

// mulDiv calcula a*b/c sin desbordar: con montos grandes y tipos en millonésimas,
// el producto no cabe en 64 bits.
func mulDiv(a, b, c int64) int64 {
	r := new(big.Int).Mul(big.NewInt(a), big.NewInt(b))
	return r.Quo(r, big.NewInt(c)).Int64()
}

// rateText: 3380000 → "3,3800".
func rateText(rate int64) string {
	return fmt.Sprintf("%d,%04d", rate/1_000_000, (rate%1_000_000)/100)
}
