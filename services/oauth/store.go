package oauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

//go:embed schema.sql
var schemaSQL string

// Ventana en la que el secreto anterior sigue sirviendo tras una rotación.
//
// Siete días: lo comprometido con Mercatus. Es tiempo de sobra para que el socio
// despliegue el secreto nuevo sin coordinar un corte simultáneo — que es
// justamente la situación en la que las rotaciones se posponen para siempre.
const SecretGraceWindow = 7 * 24 * time.Hour

var (
	ErrClientNotFound  = errors.New("cliente inexistente")
	ErrClientInactive  = errors.New("cliente desactivado")
	ErrBadSecret       = errors.New("secreto inválido")
	ErrScopeNotGranted = errors.New("scope no concedido")
)

// Client es una integración registrada.
type Client struct {
	ID              string
	Name            string
	Scopes          []string
	MasterAccountID string
	Active          bool
}

// SubAccount es una cuenta del ledger que pertenece a un agente del socio.
type SubAccount struct {
	AccountID      string
	OwnerReference string
	Currency       string
	DisplayName    string
	CreatedAt      time.Time
}

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

func (s *Store) Migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, schemaSQL)
	return err
}

// Register da de alta una integración y devuelve su secreto EN CLARO.
//
// El secreto se devuelve una sola vez, aquí: la base guarda solo su hash. Si se
// pierde, se rota; no hay forma de recuperarlo, que es la propiedad que se busca.
func (s *Store) Register(ctx context.Context, clientID, name string, scopes []string) (string, error) {
	secret, err := generateSecret()
	if err != nil {
		return "", err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO oauth.clients (client_id, name, secret_hash, scopes)
		VALUES ($1, $2, $3, $4)
	`, clientID, name, hashSecret(secret), pq.Array(scopes))
	if err != nil {
		return "", fmt.Errorf("registrar cliente: %w", err)
	}
	return secret, nil
}

// RotateSecret emite un secreto nuevo y deja el anterior vivo durante la ventana
// de gracia.
func (s *Store) RotateSecret(ctx context.Context, clientID string) (string, error) {
	secret, err := generateSecret()
	if err != nil {
		return "", err
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE oauth.clients
		   SET previous_secret_hash = secret_hash,
		       previous_expires_at  = now() + $3::interval,
		       secret_hash          = $2,
		       rotated_at           = now()
		 WHERE client_id = $1
	`, clientID, hashSecret(secret), fmt.Sprintf("%d seconds", int(SecretGraceWindow.Seconds())))
	if err != nil {
		return "", fmt.Errorf("rotar secreto: %w", err)
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return "", ErrClientNotFound
	}
	return secret, nil
}

// Authenticate valida las credenciales de una integración.
//
// Devuelve el mismo error para "cliente inexistente" y "secreto incorrecto" en el
// nivel de arriba a propósito; aquí se distinguen para el registro interno, pero
// la respuesta HTTP no puede diferenciarlos o se convierte en un oráculo de
// client_id válidos.
func (s *Store) Authenticate(ctx context.Context, clientID, secret string) (*Client, error) {
	var (
		client      Client
		hash        string
		prevHash    sql.NullString
		prevExpires sql.NullTime
		master      sql.NullString
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT client_id, name, secret_hash, previous_secret_hash, previous_expires_at,
		       scopes, master_account_id, active
		  FROM oauth.clients
		 WHERE client_id = $1
	`, clientID).Scan(
		&client.ID, &client.Name, &hash, &prevHash, &prevExpires,
		pq.Array(&client.Scopes), &master, &client.Active,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrClientNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("buscar cliente: %w", err)
	}
	if master.Valid {
		client.MasterAccountID = master.String
	}

	if !secretMatches(secret, hash, prevHash, prevExpires) {
		return nil, ErrBadSecret
	}
	if !client.Active {
		return nil, ErrClientInactive
	}
	return &client, nil
}

// secretMatches compara contra el secreto vigente y, si sigue en ventana, contra
// el anterior. La comparación es de tiempo constante: una comparación normal
// filtra por temporización cuántos caracteres del hash coincidieron.
func secretMatches(secret, hash string, prevHash sql.NullString, prevExpires sql.NullTime) bool {
	candidate := hashSecret(secret)
	if subtle.ConstantTimeCompare([]byte(candidate), []byte(hash)) == 1 {
		return true
	}
	if !prevHash.Valid || !prevExpires.Valid || time.Now().After(prevExpires.Time) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(candidate), []byte(prevHash.String)) == 1
}

// GrantScopes calcula los scopes concedidos a partir de los pedidos.
//
// Pedir un scope que el cliente no tiene es un ERROR, no un recorte silencioso:
// un cliente que cree tener permiso de escritura y recibe un token de solo
// lectura descubre el problema al intentar mover dinero, que es el peor momento.
func GrantScopes(client *Client, requested []string) ([]string, error) {
	if len(requested) == 0 {
		return client.Scopes, nil
	}
	granted := make([]string, 0, len(requested))
	for _, want := range requested {
		found := false
		for _, has := range client.Scopes {
			if has == want {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("%w: %s", ErrScopeNotGranted, want)
		}
		granted = append(granted, want)
	}
	return granted, nil
}

// ---------------------------------------------------------------- sub-cuentas

// LinkAccount asocia una cuenta del ledger a un agente del socio.
//
// Idempotente por `(client_id, owner_reference, currency)`: si el socio reintenta
// tras un timeout, recupera la cuenta que ya existe en vez de abrir una segunda y
// partir el saldo del agente en dos.
func (s *Store) LinkAccount(ctx context.Context, clientID string, sub SubAccount) (SubAccount, bool, error) {
	var existing SubAccount
	err := s.db.QueryRowContext(ctx, `
		SELECT account_id, owner_reference, currency, display_name, created_at
		  FROM oauth.client_accounts
		 WHERE client_id = $1 AND owner_reference = $2 AND currency = $3
	`, clientID, sub.OwnerReference, sub.Currency).Scan(
		&existing.AccountID, &existing.OwnerReference, &existing.Currency,
		&existing.DisplayName, &existing.CreatedAt,
	)
	if err == nil {
		return existing, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return SubAccount{}, false, fmt.Errorf("buscar sub-cuenta: %w", err)
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO oauth.client_accounts (client_id, account_id, owner_reference, currency, display_name)
		VALUES ($1, $2, $3, $4, $5)
	`, clientID, sub.AccountID, sub.OwnerReference, sub.Currency, sub.DisplayName)
	if err != nil {
		return SubAccount{}, false, fmt.Errorf("enlazar sub-cuenta: %w", err)
	}
	sub.CreatedAt = time.Now().UTC()
	return sub, false, nil
}

// OwnsAccount indica si una cuenta pertenece a la integración.
//
// Es la barrera que impide que un socio consulte o mueva el dinero de cuentas que
// no son suyas conociendo un identificador ajeno.
func (s *Store) OwnsAccount(ctx context.Context, clientID, accountID string) (bool, error) {
	if _, err := uuid.Parse(accountID); err != nil {
		return false, nil
	}
	var exists bool
	err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM oauth.client_accounts WHERE client_id = $1 AND account_id = $2
		)
	`, clientID, accountID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("verificar propiedad de cuenta: %w", err)
	}
	return exists, nil
}

// SetMasterAccount fija la cuenta maestra de la integración.
func (s *Store) SetMasterAccount(ctx context.Context, clientID, accountID string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE oauth.clients SET master_account_id = $2 WHERE client_id = $1
	`, clientID, accountID)
	return err
}

// ---------------------------------------------------------------- secretos

// generateSecret produce un secreto de 256 bits de entropía criptográfica.
func generateSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generar secreto: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}
