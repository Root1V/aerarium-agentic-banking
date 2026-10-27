package passkeys

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
)

//go:embed schema.sql
var schema string

// Store persiste passkeys, ceremonias y sesiones.
type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

func (s *Store) Migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, schema)
	return err
}

type ceremonyKind string

const (
	kindEnrollment ceremonyKind = "enrollment"
	kindAddDevice  ceremonyKind = "add_device"
	kindLogin      ceremonyKind = "login"
)

type ceremony struct {
	kind       ceremonyKind
	customerID string
	userName   string
	session    webauthn.SessionData
}

func (s *Store) saveCeremony(ctx context.Context, id string, c ceremony, expires time.Time) error {
	raw, err := json.Marshal(c.session)
	if err != nil {
		return err
	}
	var customer any
	if c.customerID != "" {
		customer = c.customerID
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO identity.ceremonies (id, kind, customer_id, user_name, session, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		id, string(c.kind), customer, c.userName, raw, expires)
	return err
}

// consumeCeremony entrega la ceremonia y la marca usada, en una sola sentencia.
//
// Es la defensa contra la repetición: dos peticiones con el mismo desafío no
// pueden pasar las dos, porque solo una encuentra `consumed_at` vacío. Una
// ceremonia vencida, ya usada o de otro tipo da lo mismo que una inexistente.
func (s *Store) consumeCeremony(ctx context.Context, id string, kinds ...ceremonyKind) (*ceremony, error) {
	var (
		kind       string
		customerID sql.NullString
		userName   sql.NullString
		raw        []byte
	)
	err := s.db.QueryRowContext(ctx, `
		UPDATE identity.ceremonies
		   SET consumed_at = now()
		 WHERE id = $1 AND consumed_at IS NULL AND expires_at > now()
		RETURNING kind, customer_id, user_name, session`, id).
		Scan(&kind, &customerID, &userName, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCeremonyNotFound
	}
	if err != nil {
		return nil, err
	}

	allowed := false
	for _, k := range kinds {
		allowed = allowed || ceremonyKind(kind) == k
	}
	if !allowed {
		// Un desafío de login no sirve para registrar, ni al revés.
		return nil, ErrCeremonyNotFound
	}

	c := &ceremony{kind: ceremonyKind(kind), customerID: customerID.String, userName: userName.String}
	if err := json.Unmarshal(raw, &c.session); err != nil {
		return nil, fmt.Errorf("sesión de ceremonia ilegible: %w", err)
	}
	return c, nil
}

type storedPasskey struct {
	customerID string
	userName   string
	credential webauthn.Credential
}

func (s *Store) passkeysOf(ctx context.Context, customerID string) ([]storedPasskey, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT customer_id, user_name, credential FROM identity.passkeys
		 WHERE customer_id = $1 ORDER BY created_at`, customerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPasskeys(rows)
}

func (s *Store) passkeyByCredential(ctx context.Context, credentialID []byte) (*storedPasskey, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT customer_id, user_name, credential FROM identity.passkeys
		 WHERE credential_id = $1`, credentialID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found, err := scanPasskeys(rows)
	if err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, ErrUnknownCredential
	}
	return &found[0], nil
}

func scanPasskeys(rows *sql.Rows) ([]storedPasskey, error) {
	var out []storedPasskey
	for rows.Next() {
		var (
			p   storedPasskey
			raw []byte
		)
		if err := rows.Scan(&p.customerID, &p.userName, &raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &p.credential); err != nil {
			return nil, fmt.Errorf("passkey ilegible: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// insertPasskey guarda una passkey nueva.
//
// Con `onlyIfFirst`, solo si el cliente no tiene ninguna. Se comprueba en la
// misma sentencia que inserta: dos enrolamientos simultáneos con la misma
// referencia no pueden dejar dos passkeys, porque la segunda ya ve la primera.
func (s *Store) insertPasskey(ctx context.Context, customerID, userName string, cred *webauthn.Credential, onlyIfFirst bool) error {
	raw, err := json.Marshal(cred)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO identity.passkeys (credential_id, customer_id, user_name, credential)
		SELECT $1, $2, $3, $4
		 WHERE NOT $5 OR NOT EXISTS (SELECT 1 FROM identity.passkeys WHERE customer_id = $2)
		ON CONFLICT (credential_id) DO NOTHING`,
		cred.ID, customerID, userName, raw, onlyIfFirst)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrAlreadyEnrolled
	}
	return nil
}

func (s *Store) hasPasskey(ctx context.Context, customerID string) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM identity.passkeys WHERE customer_id = $1)`, customerID).Scan(&exists)
	return exists, err
}

// updateAfterLogin guarda el contador y las banderas que trajo el login.
//
// El contador importa: si una passkey firma con un contador menor que el último
// visto, alguien la clonó. Para detectarlo, el último visto tiene que estar
// guardado.
func (s *Store) updateAfterLogin(ctx context.Context, cred *webauthn.Credential) error {
	raw, err := json.Marshal(cred)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		UPDATE identity.passkeys SET credential = $2, last_used_at = now()
		 WHERE credential_id = $1`, cred.ID, raw)
	return err
}
