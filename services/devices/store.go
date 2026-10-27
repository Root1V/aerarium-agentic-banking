package devices

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"time"
)

//go:embed schema.sql
var schema string

// Store persiste dispositivos y desafíos.
type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

func (s *Store) Migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, schema)
	return err
}

type purpose string

const (
	purposeRegister purpose = "register"
	purposeLogin    purpose = "login"
)

type device struct {
	id            string
	customerID    string
	biometricKey  []byte
	possessionKey []byte
	pinHash       []byte
	pinSalt       []byte
	locked        bool
	revoked       bool
}

func (s *Store) saveChallenge(ctx context.Context, id string, p purpose, deviceID string, payload []byte, expires time.Time) error {
	var dev any
	if deviceID != "" {
		dev = deviceID
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO identity.device_challenges (id, purpose, device_id, payload, expires_at)
		VALUES ($1, $2, $3, $4, $5)`, id, string(p), dev, payload, expires)
	return err
}

// consumeChallenge entrega el desafío y lo marca usado en la misma sentencia:
// dos peticiones con el mismo desafío no pueden pasar las dos. Uno vencido, ya
// usado o de otro propósito da lo mismo que uno inexistente.
func (s *Store) consumeChallenge(ctx context.Context, id string, p purpose) (payload []byte, deviceID string, err error) {
	var dev sql.NullString
	err = s.db.QueryRowContext(ctx, `
		UPDATE identity.device_challenges
		   SET consumed_at = now()
		 WHERE id = $1 AND purpose = $2 AND consumed_at IS NULL AND expires_at > now()
		RETURNING payload, device_id`, id, string(p)).Scan(&payload, &dev)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", ErrChallengeNotFound
	}
	return payload, dev.String, err
}

func (s *Store) deviceByID(ctx context.Context, id string) (*device, error) {
	d := device{id: id}
	err := s.db.QueryRowContext(ctx, `
		SELECT customer_id, biometric_key, possession_key, pin_hash, pin_salt,
		       locked_at IS NOT NULL, revoked_at IS NOT NULL
		  FROM identity.devices WHERE id = $1`, id).
		Scan(&d.customerID, &d.biometricKey, &d.possessionKey, &d.pinHash, &d.pinSalt, &d.locked, &d.revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrRejected
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

type newDevice struct {
	id, customerID, name        string
	biometricKey, possessionKey []byte
	pinHash, pinSalt            []byte
	hardwareBacked              bool
}

// insertDevice vincula un teléfono.
//
// Con `onlyIfFirst`, solo si el cliente nunca tuvo ninguno —ni siquiera uno ya
// desvinculado—. Se comprueba en la misma sentencia que inserta: dos altas
// simultáneas con la misma referencia no pueden dejar dos teléfonos.
func (s *Store) insertDevice(ctx context.Context, d newDevice, onlyIfFirst bool) error {
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO identity.devices
		       (id, customer_id, name, biometric_key, possession_key, pin_hash, pin_salt, hardware_backed)
		SELECT $1, $2, $3, $4, $5, $6, $7, $8
		 WHERE NOT $9 OR NOT EXISTS (SELECT 1 FROM identity.devices WHERE customer_id = $2)`,
		d.id, d.customerID, d.name, d.biometricKey, d.possessionKey, d.pinHash, d.pinSalt, d.hardwareBacked, onlyIfFirst)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrNotEnrollable
	}
	return nil
}

func (s *Store) markUsed(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE identity.devices SET last_used_at = now(), failed_pin_attempts = 0 WHERE id = $1`, id)
	return err
}

// failPIN suma un intento fallido y bloquea el dispositivo al llegar al tope.
// Atómico: diez intentos simultáneos no pueden colarse todos por debajo del tope.
func (s *Store) failPIN(ctx context.Context, id string, max int) (attempts int, err error) {
	err = s.db.QueryRowContext(ctx, `
		UPDATE identity.devices
		   SET failed_pin_attempts = failed_pin_attempts + 1,
		       locked_at = CASE WHEN failed_pin_attempts + 1 >= $2 THEN now() ELSE locked_at END
		 WHERE id = $1
		RETURNING failed_pin_attempts`, id, max).Scan(&attempts)
	return attempts, err
}

func (s *Store) revoke(ctx context.Context, customerID, id string) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE identity.devices SET revoked_at = now()
		 WHERE id = $1 AND customer_id = $2 AND revoked_at IS NULL`, id, customerID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrRejected
	}
	return nil
}
