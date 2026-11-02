// Package receipts emite y verifica constancias de operación.
//
// En el Perú abundan las estafas con capturas falsas de un pago: el estafador
// muestra un «comprobante» hecho con una app alterada o un editor de imágenes, y
// quien vende no tiene cómo saber si es real. Una constancia de Aerarium lleva un
// código; con él, cualquiera —sea o no cliente— le pregunta al banco si ese pago
// existió, por cuánto, entre quiénes y cuándo.
package receipts

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"errors"
	"strings"
	"time"
)

//go:embed schema.sql
var schema string

// ErrNotFound: no hay una constancia con ese código.
var ErrNotFound = errors.New("no existe una constancia con ese código")

// Receipt es lo que dice una constancia.
type Receipt struct {
	Code          string
	TransactionID string
	AccountID     string
	CustomerID    string
	Kind          string
	AmountMicros  int64
	Currency      string
	// Los nombres, acortados: bastan para reconocer a alguien, no para más.
	FromName string
	ToName   string
	PostedAt time.Time
	IssuedAt time.Time
}

type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

func (s *Store) Migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, schema)
	return err
}

// Issue emite la constancia de un movimiento, o devuelve la que ya tenía.
func (s *Store) Issue(ctx context.Context, r Receipt) (*Receipt, error) {
	code, err := newCode()
	if err != nil {
		return nil, err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO receipts.receipts
			(code, transaction_id, account_id, customer_id, kind, amount_micros, currency, from_name, to_name, posted_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (transaction_id, account_id) DO NOTHING`,
		code, r.TransactionID, r.AccountID, r.CustomerID, r.Kind, r.AmountMicros, r.Currency,
		r.FromName, r.ToName, r.PostedAt)
	if err != nil {
		return nil, err
	}
	return s.find(ctx, `transaction_id = $1::uuid AND account_id = $2::uuid`, r.TransactionID, r.AccountID)
}

// Verify busca la constancia por su código. El código se acepta con o sin
// guion y en minúsculas: se dicta por teléfono y se escribe a mano.
func (s *Store) Verify(ctx context.Context, code string) (*Receipt, error) {
	return s.find(ctx, `code = $1`, Normalize(code))
}

func (s *Store) find(ctx context.Context, where string, args ...any) (*Receipt, error) {
	var r Receipt
	err := s.db.QueryRowContext(ctx, `
		SELECT code, transaction_id, account_id, customer_id, kind, amount_micros, currency,
		       from_name, to_name, posted_at, issued_at
		  FROM receipts.receipts WHERE `+where, args...).
		Scan(&r.Code, &r.TransactionID, &r.AccountID, &r.CustomerID, &r.Kind, &r.AmountMicros, &r.Currency,
			&r.FromName, &r.ToName, &r.PostedAt, &r.IssuedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &r, err
}

// alphabet: Crockford base32, sin I, L, O ni U, que se confunden al dictarlo.
const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// newCode son 10 caracteres (50 bits), como XXXXX-XXXXX: imposible de adivinar
// y fácil de dictar.
func newCode() (string, error) {
	raw := make([]byte, 10)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	var b strings.Builder
	for i, v := range raw {
		if i == 5 {
			b.WriteByte('-')
		}
		b.WriteByte(alphabet[int(v)%len(alphabet)])
	}
	return b.String(), nil
}

// Normalize lleva un código escrito a mano a su forma: mayúsculas, con guion,
// y las letras que se confunden, a su dígito.
func Normalize(code string) string {
	clean := strings.Map(func(r rune) rune {
		switch r {
		case 'O', 'o':
			return '0'
		case 'I', 'i', 'L', 'l':
			return '1'
		case '-', ' ':
			return -1
		}
		if r >= 'a' && r <= 'z' {
			return r - 'a' + 'A'
		}
		return r
	}, code)
	if len(clean) != 10 {
		return clean
	}
	return clean[:5] + "-" + clean[5:]
}
