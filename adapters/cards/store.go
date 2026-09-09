package cards

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"time"
)

//go:embed schema.sql
var schemaSQL string

// Estados de una autorización.
const (
	statusHeld     = "held"
	statusCleared  = "cleared"
	statusReversed = "reversed"
)

// Authorization es el registro propio del emisor sobre una compra.
type Authorization struct {
	NetworkTransactionID string
	CardID               string
	AccountID            string
	AmountMicros          int64
	Currency             string
	MerchantName         string
	Status               string
	HoldTransactionID    string
	ClearedAmountMicros   int64
	OverageMicros         int64
	AuthorizedAt         time.Time
}

func (a Authorization) isHeld() bool { return a.Status == statusHeld }

// Store persiste autorizaciones.
type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("aplicar esquema de tarjetas: %w", err)
	}
	return nil
}

// reserve registra la autorización si es nueva.
//
// Devuelve `created=false` cuando la red repite una compra ya conocida, que es lo
// que permite responder lo mismo sin volver a retener dinero.
func (s *Store) reserve(ctx context.Context, auth Authorization) (created bool, existing *Authorization, err error) {
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO cards.authorizations
			(network_transaction_id, card_id, account_id, amount_micros, currency,
			 merchant_name, status)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (network_transaction_id) DO NOTHING`,
		auth.NetworkTransactionID, auth.CardID, auth.AccountID, auth.AmountMicros,
		auth.Currency, auth.MerchantName, statusHeld)
	if err != nil {
		return false, nil, fmt.Errorf("reservar autorización: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return false, nil, fmt.Errorf("reservar autorización: %w", err)
	}
	if rows == 1 {
		return true, nil, nil
	}

	found, err := s.Get(ctx, auth.NetworkTransactionID)
	if err != nil {
		return false, nil, err
	}
	return false, found, nil
}

func (s *Store) Get(ctx context.Context, networkTransactionID string) (*Authorization, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT network_transaction_id, card_id, account_id::text, amount_micros, currency,
		       COALESCE(merchant_name,''), status, COALESCE(hold_transaction_id::text,''),
		       COALESCE(cleared_amount_micros, 0), overage_micros, authorized_at
		FROM cards.authorizations WHERE network_transaction_id = $1`, networkTransactionID)

	var a Authorization
	err := row.Scan(&a.NetworkTransactionID, &a.CardID, &a.AccountID, &a.AmountMicros,
		&a.Currency, &a.MerchantName, &a.Status, &a.HoldTransactionID,
		&a.ClearedAmountMicros, &a.OverageMicros, &a.AuthorizedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUnknownAuthorization
	}
	if err != nil {
		return nil, fmt.Errorf("leer autorización: %w", err)
	}
	return &a, nil
}

func (s *Store) markHeld(ctx context.Context, networkTransactionID, holdTransactionID string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE cards.authorizations SET hold_transaction_id = $2
		WHERE network_transaction_id = $1`, networkTransactionID, holdTransactionID)
	if err != nil {
		return fmt.Errorf("registrar retención: %w", err)
	}
	return nil
}

// drop borra una autorización que no llegó a retener dinero. Solo se usa cuando
// el asiento falló: dejarla como retenida mostraría un saldo retenido inexistente.
func (s *Store) drop(ctx context.Context, networkTransactionID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM cards.authorizations WHERE network_transaction_id = $1 AND hold_transaction_id IS NULL`,
		networkTransactionID)
	if err != nil {
		return fmt.Errorf("descartar autorización: %w", err)
	}
	return nil
}

func (s *Store) markCleared(ctx context.Context, networkTransactionID string, finalAmount, overage int64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE cards.authorizations
		SET status = $2, cleared_amount_micros = $3, overage_micros = $4, resolved_at = now()
		WHERE network_transaction_id = $1`,
		networkTransactionID, statusCleared, finalAmount, overage)
	if err != nil {
		return fmt.Errorf("registrar cobro: %w", err)
	}
	return nil
}

func (s *Store) markReversed(ctx context.Context, networkTransactionID string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE cards.authorizations SET status = $2, resolved_at = now()
		WHERE network_transaction_id = $1`, networkTransactionID, statusReversed)
	if err != nil {
		return fmt.Errorf("registrar reversa: %w", err)
	}
	return nil
}

// HeldOlderThan lista las retenciones vivas más antiguas que `age`.
//
// Una retención que nadie cobra inmoviliza dinero del cliente: pasado el plazo de
// la red debe liberarse. Es la cola que alimenta ese proceso.
func (s *Store) HeldOlderThan(ctx context.Context, age time.Duration) ([]Authorization, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT network_transaction_id, card_id, account_id::text, amount_micros, currency,
		       COALESCE(merchant_name,''), status, COALESCE(hold_transaction_id::text,''),
		       COALESCE(cleared_amount_micros, 0), overage_micros, authorized_at
		FROM cards.authorizations
		WHERE status = 'held' AND authorized_at < now() - $1::interval
		ORDER BY authorized_at`, fmt.Sprintf("%d seconds", int(age.Seconds())))
	if err != nil {
		return nil, fmt.Errorf("listar retenciones vencidas: %w", err)
	}
	defer rows.Close()

	var out []Authorization
	for rows.Next() {
		var a Authorization
		if err := rows.Scan(&a.NetworkTransactionID, &a.CardID, &a.AccountID, &a.AmountMicros,
			&a.Currency, &a.MerchantName, &a.Status, &a.HoldTransactionID,
			&a.ClearedAmountMicros, &a.OverageMicros, &a.AuthorizedAt); err != nil {
			return nil, fmt.Errorf("leer retención: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
