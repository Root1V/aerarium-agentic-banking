// Package sessions emite y resuelve las sesiones del canal del titular.
//
// Es la pieza común a todas las formas de entrar. Quien verifica a la persona
// —las passkeys, las llaves del teléfono— decide si entra; este paquete solo
// recuerda que entró, con qué factor y desde qué dispositivo, durante un rato.
package sessions

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"
)

//go:embed schema.sql
var schema string

// ErrInvalid: la sesión no existe, venció o se cerró.
var ErrInvalid = errors.New("sesión inválida")

// Factor es cómo demostró la persona que era ella.
type Factor string

const (
	FactorPasskey         Factor = "passkey"
	FactorDeviceBiometric Factor = "device_biometric"
	FactorDevicePIN       Factor = "device_pin"
)

// Session es lo que se le entrega a la app.
type Session struct {
	Token      string
	CustomerID string
	ExpiresAt  time.Time
}

// Principal es lo que hay detrás de un token válido.
type Principal struct {
	CustomerID string
	Factor     Factor
	DeviceRef  string
}

type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

func (s *Store) Migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, schema)
	return err
}

// Open emite una sesión nueva.
func (s *Store) Open(ctx context.Context, customerID string, factor Factor, deviceRef string, expires time.Time) (*Session, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO identity.app_sessions (token_hash, customer_id, factor, device_ref, expires_at)
		VALUES ($1, $2, $3, $4, $5)`, hashToken(token), customerID, string(factor), deviceRef, expires)
	if err != nil {
		return nil, err
	}
	return &Session{Token: token, CustomerID: customerID, ExpiresAt: expires}, nil
}

func (s *Store) Resolve(ctx context.Context, token string) (*Principal, error) {
	var p Principal
	err := s.db.QueryRowContext(ctx, `
		SELECT customer_id, factor, device_ref FROM identity.app_sessions
		 WHERE token_hash = $1 AND revoked_at IS NULL AND expires_at > now()`,
		hashToken(token)).Scan(&p.CustomerID, &p.Factor, &p.DeviceRef)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrInvalid
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// Revoke cierra una sesión. Cerrar una que ya no existe no es un error.
func (s *Store) Revoke(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE identity.app_sessions SET revoked_at = now()
		 WHERE token_hash = $1 AND revoked_at IS NULL`, hashToken(token))
	return err
}

// RevokeDevice cierra todas las sesiones abiertas desde un dispositivo: al
// desvincularlo, lo que ya estaba abierto en él tampoco puede seguir.
func (s *Store) RevokeDevice(ctx context.Context, deviceRef string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE identity.app_sessions SET revoked_at = now()
		 WHERE device_ref = $1 AND revoked_at IS NULL`, deviceRef)
	return err
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
