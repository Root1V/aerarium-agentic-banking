// Package stepup decide cuándo una operación pide confirmación reforzada.
//
// Entrar a la app ya cubrió dos factores, pero una sesión abierta no debería
// bastar para todo: un teléfono desbloqueado en manos de otro, o una persona
// engañada por teléfono, pueden mover dinero con ella. Las operaciones de riesgo
// piden además firmar ESA operación con la cara (doc 14; Res. SBS 2286-2024).
//
// Aquí solo vive la política —qué es de riesgo— y la memoria de destinatarios
// conocidos. La firma la verifica el paquete devices.
package stepup

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"strconv"
)

//go:embed schema.sql
var schema string

// Reason es por qué una operación pide confirmación. Vacía: no la pide.
type Reason string

const (
	ReasonNone     Reason = ""
	ReasonNewPayee Reason = "new_payee"
	ReasonAmount   Reason = "amount"
)

// Policy son los umbrales.
type Policy struct {
	// AmountThreshold en micras: desde este monto se confirma siempre, aunque el
	// destinatario sea conocido.
	AmountThreshold int64
}

// DefaultPolicy: 200 de la moneda de la cuenta.
//
// Por debajo del tope por operación de la cuenta de nivel 1 (300): un umbral
// más alto no se aplicaría nunca a quien acaba de abrir su cuenta, que es
// justamente a quien más se intenta engañar.
func DefaultPolicy() Policy { return Policy{AmountThreshold: 200_000_000} }

type Service struct {
	db     *sql.DB
	policy Policy
}

func NewService(db *sql.DB, policy Policy) *Service { return &Service{db: db, policy: policy} }

func (s *Service) Migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, schema)
	return err
}

// Transfer describe una transferencia tal como se firma.
type Transfer struct {
	CustomerID     string
	FromAccountID  string
	ToAccountID    string
	AmountMicros   int64
	Currency       string
	IdempotencyKey string
}

// Operation es la descripción canónica que va dentro de lo que firma el
// teléfono. Incluye la clave de idempotencia: la confirmación vale para esta
// operación y no para otra idéntica que se pida después.
func (t Transfer) Operation() []byte {
	return []byte("transfer" +
		"\nfrom=" + t.FromAccountID +
		"\nto=" + t.ToAccountID +
		"\namount=" + strconv.FormatInt(t.AmountMicros, 10) +
		"\ncurrency=" + t.Currency +
		"\nkey=" + t.IdempotencyKey)
}

// Required dice si la transferencia pide confirmación, y por qué.
func (s *Service) Required(ctx context.Context, t Transfer) (Reason, error) {
	if t.AmountMicros >= s.policy.AmountThreshold {
		return ReasonAmount, nil
	}
	var known bool
	err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM identity.known_payees WHERE customer_id = $1 AND account_id = $2)`,
		t.CustomerID, t.ToAccountID).Scan(&known)
	if err != nil {
		return ReasonNone, fmt.Errorf("destinatarios conocidos: %w", err)
	}
	if !known {
		return ReasonNewPayee, nil
	}
	return ReasonNone, nil
}

// Remember anota el destinatario como conocido tras una transferencia confirmada.
func (s *Service) Remember(ctx context.Context, customerID, accountID string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO identity.known_payees (customer_id, account_id) VALUES ($1, $2)
		ON CONFLICT DO NOTHING`, customerID, accountID)
	return err
}
