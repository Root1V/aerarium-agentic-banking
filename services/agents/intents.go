package agents

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"unicode"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

// ErrAccountBound: la sub-cuenta ya es de otro agente.
var ErrAccountBound = errors.New("la cuenta ya pertenece a otro agente")

// ErrBadIntent: la intención no cumple el contrato.
var ErrBadIntent = errors.New("intent inválido: reference (1–128) y description (1–140, texto plano) son obligatorios")

// Intent es por qué se hizo un pago (AE-10 y AE-12).
type Intent struct {
	// Reference es el identificador de la tarea que el cliente le dio al agente,
	// en la plataforma. Varios pagos de una tarea comparten la misma.
	Reference string `json:"reference"`
	// Description es qué se compró, en texto plano. El titular la lee tal cual.
	Description string `json:"description"`
}

// Validate comprueba la intención. Texto plano: sin caracteres de control, que
// en un historial podrían esconder o desordenar lo que se muestra.
func (i Intent) Validate() error {
	ref, desc := strings.TrimSpace(i.Reference), strings.TrimSpace(i.Description)
	if ref == "" || len([]rune(ref)) > 128 || desc == "" || len([]rune(desc)) > 140 {
		return ErrBadIntent
	}
	for _, r := range ref + desc {
		if unicode.IsControl(r) {
			return ErrBadIntent
		}
	}
	return nil
}

// BindAccount ata una sub-cuenta a un agente vivo de la integración. Repetirlo
// con el mismo agente no es un error; con otro, sí.
func (s *Store) BindAccount(ctx context.Context, clientID, agentID, accountID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockLiveAgent(ctx, tx, clientID, agentID); err != nil {
		return err
	}
	var owner string
	err = tx.QueryRowContext(ctx, `
		INSERT INTO agents.accounts (account_id, agent_id) VALUES ($1, $2)
		ON CONFLICT (account_id) DO UPDATE SET account_id = EXCLUDED.account_id
		RETURNING agent_id`, accountID, agentID).Scan(&owner)
	if err != nil {
		return err
	}
	if owner != agentID {
		return ErrAccountBound
	}
	return tx.Commit()
}

// AccountAgent dice de qué agente es una sub-cuenta; vacío si no es de ninguno.
func (s *Store) AccountAgent(ctx context.Context, accountID string) (string, error) {
	var agentID string
	err := s.db.QueryRowContext(ctx,
		`SELECT agent_id FROM agents.accounts WHERE account_id = $1`, accountID).Scan(&agentID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return agentID, err
}

// RecordIntent guarda por qué se hizo un pago. Un reintento del mismo pago no
// cambia la que ya quedó.
func (s *Store) RecordIntent(ctx context.Context, authorizationID, agentID, clientID string, i Intent) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO agents.payment_intents (authorization_id, agent_id, client_id, reference, description)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (authorization_id) DO NOTHING`,
		authorizationID, agentID, clientID, strings.TrimSpace(i.Reference), strings.TrimSpace(i.Description))
	return err
}

// PaymentIntent es la intención de un pago, con el agente que lo hizo.
type PaymentIntent struct {
	Intent
	AgentID   string
	AgentName string
}

// IntentsFor devuelve la intención de cada autorización que la tenga.
func (s *Store) IntentsFor(ctx context.Context, authorizationIDs []string) (map[string]PaymentIntent, error) {
	ids := make([]string, 0, len(authorizationIDs))
	for _, id := range authorizationIDs {
		if _, err := uuid.Parse(id); err == nil {
			ids = append(ids, id)
		}
	}
	out := map[string]PaymentIntent{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT i.authorization_id, i.reference, i.description, a.id, a.name
		  FROM agents.payment_intents i JOIN agents.agents a ON a.id = i.agent_id
		 WHERE i.authorization_id = ANY($1::uuid[])`, pq.Array(ids))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var p PaymentIntent
		if err := rows.Scan(&id, &p.Reference, &p.Description, &p.AgentID, &p.AgentName); err != nil {
			return nil, err
		}
		out[id] = p
	}
	return out, rows.Err()
}
