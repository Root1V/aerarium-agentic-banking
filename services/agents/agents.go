// Package agents registra a los agentes de cada plataforma y las claves con las
// que firman sus pagos (F3-05, acordado con Mercatus en AE-10 y AE-11).
//
// La plataforma se autentica con su credencial OAuth y actúa por todos sus
// agentes. Lo que agrega este paquete es que cada agente tiene identidad propia:
// un nombre que el titular reconoce al darle permiso, y una clave Ed25519 con la
// que firma cada pago. Así el banco sabe qué agente pagó, y puede probarlo.
package agents

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

//go:embed schema.sql
var schema string

// RotationWindow es cuánto sigue valiendo una clave después de añadir otra. Una
// rotación que se queda a medias no deja la clave vieja viva para siempre.
const RotationWindow = 7 * 24 * time.Hour

var (
	ErrNotFound   = errors.New("agente o clave inexistente")
	ErrRevoked    = errors.New("el agente está dado de baja")
	ErrInvalidKey = errors.New("clave pública inválida: se espera un JWK Ed25519")
	ErrKeyInUse   = errors.New("esa clave ya está registrada")
	ErrLastKey    = errors.New("no se puede retirar la única clave vigente; añade otra antes")
	ErrBadName    = errors.New("el nombre es obligatorio, de hasta 80 caracteres")
)

// JWK es una clave pública Ed25519 en el formato de RFC 8037.
type JWK struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	X   string `json:"x"`
}

// PublicKey valida el JWK y devuelve la clave.
func (k JWK) PublicKey() (ed25519.PublicKey, error) {
	if k.Kty != "OKP" || k.Crv != "Ed25519" {
		return nil, ErrInvalidKey
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(k.X, "="))
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, ErrInvalidKey
	}
	return ed25519.PublicKey(raw), nil
}

// ToJWK expresa una clave pública como JWK.
func ToJWK(pub ed25519.PublicKey) JWK {
	return JWK{Kty: "OKP", Crv: "Ed25519", X: base64.RawURLEncoding.EncodeToString(pub)}
}

type Agent struct {
	ID        string
	ClientID  string
	Name      string
	CreatedAt time.Time
	RevokedAt *time.Time
	Keys      []Key
}

type Key struct {
	ID        string
	PublicKey ed25519.PublicKey
	CreatedAt time.Time
	ExpiresAt *time.Time
	RetiredAt *time.Time
}

// Valid dice si la clave sirve para firmar ahora.
func (k Key) Valid(now time.Time) bool {
	return k.RetiredAt == nil && (k.ExpiresAt == nil || now.Before(*k.ExpiresAt))
}

type Store struct {
	db  *sql.DB
	now func() time.Time
}

func NewStore(db *sql.DB) *Store { return &Store{db: db, now: time.Now} }

// SetClock cambia el reloj, para probar la rotación sin esperar siete días.
func (s *Store) SetClock(now func() time.Time) { s.now = now }

func (s *Store) Migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, schema)
	return err
}

// Register da de alta un agente de una integración con su primera clave.
func (s *Store) Register(ctx context.Context, clientID, name string, key JWK) (*Agent, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 80 {
		return nil, ErrBadName
	}
	pub, err := key.PublicKey()
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	id := uuid.NewString()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO agents.agents (id, client_id, name, created_at) VALUES ($1, $2, $3, $4)`,
		id, clientID, name, s.now()); err != nil {
		return nil, err
	}
	if err := insertKey(ctx, tx, id, pub, s.now()); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.Get(ctx, clientID, id)
}

// Get devuelve un agente de la integración. El de otra no existe para ella.
func (s *Store) Get(ctx context.Context, clientID, id string) (*Agent, error) {
	if _, err := uuid.Parse(id); err != nil {
		return nil, ErrNotFound
	}
	var a Agent
	err := s.db.QueryRowContext(ctx, `
		SELECT id, client_id, name, created_at, revoked_at FROM agents.agents
		 WHERE id = $1 AND client_id = $2`, id, clientID).
		Scan(&a.ID, &a.ClientID, &a.Name, &a.CreatedAt, &a.RevokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, public_key, created_at, expires_at, retired_at FROM agents.keys
		 WHERE agent_id = $1 ORDER BY created_at, id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k Key
		var pub []byte
		if err := rows.Scan(&k.ID, &pub, &k.CreatedAt, &k.ExpiresAt, &k.RetiredAt); err != nil {
			return nil, err
		}
		k.PublicKey = pub
		a.Keys = append(a.Keys, k)
	}
	return &a, rows.Err()
}

// Revoke da de baja al agente. Es definitivo: sus claves dejan de servir, y con
// ellas los permisos que tenía.
func (s *Store) Revoke(ctx context.Context, clientID, id string) error {
	if _, err := uuid.Parse(id); err != nil {
		return ErrNotFound
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE agents.agents SET revoked_at = COALESCE(revoked_at, $3)
		 WHERE id = $1 AND client_id = $2`, id, clientID, s.now())
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// AddKey añade una clave al agente. Las que ya valían siguen valiendo hasta
// RotationWindow, para que la plataforma cambie de clave sin cortar nada.
func (s *Store) AddKey(ctx context.Context, clientID, agentID string, key JWK) (*Key, error) {
	pub, err := key.PublicKey()
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockLiveAgent(ctx, tx, clientID, agentID); err != nil {
		return nil, err
	}
	now := s.now()
	if _, err := tx.ExecContext(ctx, `
		UPDATE agents.keys SET expires_at = $2
		 WHERE agent_id = $1 AND retired_at IS NULL AND (expires_at IS NULL OR expires_at > $2)`,
		agentID, now.Add(RotationWindow)); err != nil {
		return nil, err
	}
	if err := insertKey(ctx, tx, agentID, pub, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	a, err := s.Get(ctx, clientID, agentID)
	if err != nil {
		return nil, err
	}
	k := a.Keys[len(a.Keys)-1]
	return &k, nil
}

// RetireKey retira una clave antes de que venza. La única vigente no se puede
// retirar: dejaría al agente sin poder pagar y a sus permisos sin uso. Para eso
// está dar de baja al agente.
func (s *Store) RetireKey(ctx context.Context, clientID, agentID, keyID string) error {
	if _, err := uuid.Parse(keyID); err != nil {
		return ErrNotFound
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockLiveAgent(ctx, tx, clientID, agentID); err != nil {
		return err
	}
	now := s.now()
	var target bool
	var others int
	err = tx.QueryRowContext(ctx, `
		SELECT COALESCE(bool_or(id = $2), false),
		       count(*) FILTER (WHERE id <> $2)
		  FROM agents.keys
		 WHERE agent_id = $1 AND retired_at IS NULL AND (expires_at IS NULL OR expires_at > $3)`,
		agentID, keyID, now).Scan(&target, &others)
	if err != nil {
		return err
	}
	if !target {
		return ErrNotFound
	}
	if others == 0 {
		return ErrLastKey
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE agents.keys SET retired_at = $2 WHERE id = $1`, keyID, now); err != nil {
		return err
	}
	return tx.Commit()
}

// Signer es quién firmó: el agente dueño de la clave, y su integración.
type Signer struct {
	// KeyID es la clave con la que firmó: queda como evidencia del pago.
	KeyID     string
	AgentID   string
	ClientID  string
	AgentName string
	PublicKey ed25519.PublicKey
}

// KeyForSignature resuelve la clave con la que verificar una firma. Solo
// devuelve claves que sirven ahora, de agentes que no están dados de baja.
func (s *Store) KeyForSignature(ctx context.Context, keyID string) (*Signer, error) {
	if _, err := uuid.Parse(keyID); err != nil {
		return nil, ErrNotFound
	}
	sg := Signer{KeyID: keyID}
	var pub []byte
	var expires, retired, revoked *time.Time
	err := s.db.QueryRowContext(ctx, `
		SELECT a.id, a.client_id, a.name, k.public_key, k.expires_at, k.retired_at, a.revoked_at
		  FROM agents.keys k JOIN agents.agents a ON a.id = k.agent_id
		 WHERE k.id = $1`, keyID).
		Scan(&sg.AgentID, &sg.ClientID, &sg.AgentName, &pub, &expires, &retired, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if revoked != nil {
		return nil, ErrRevoked
	}
	if !(Key{ExpiresAt: expires, RetiredAt: retired}).Valid(s.now()) {
		return nil, ErrNotFound
	}
	sg.PublicKey = pub
	return &sg, nil
}

func lockLiveAgent(ctx context.Context, tx *sql.Tx, clientID, agentID string) error {
	if _, err := uuid.Parse(agentID); err != nil {
		return ErrNotFound
	}
	var revoked *time.Time
	err := tx.QueryRowContext(ctx, `
		SELECT revoked_at FROM agents.agents WHERE id = $1 AND client_id = $2 FOR UPDATE`,
		agentID, clientID).Scan(&revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if revoked != nil {
		return ErrRevoked
	}
	return nil
}

func insertKey(ctx context.Context, tx *sql.Tx, agentID string, pub ed25519.PublicKey, now time.Time) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO agents.keys (id, agent_id, public_key, created_at) VALUES ($1, $2, $3, $4)`,
		uuid.NewString(), agentID, []byte(pub), now)
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && pqErr.Code == "23505" {
		return ErrKeyInUse
	}
	if err != nil {
		return fmt.Errorf("guardar clave: %w", err)
	}
	return nil
}

// SandboxAgent devuelve el agente de prueba de una integración del sandbox, y lo
// registra la primera vez. Su clave privada no se guarda en ninguna parte: sirve
// para que el titular practique dar un permiso a un agente, no para pagar.
func (s *Store) SandboxAgent(ctx context.Context, clientID, name string) (*Agent, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `
		SELECT id FROM agents.agents
		 WHERE client_id = $1 AND name = $2 AND revoked_at IS NULL
		 ORDER BY created_at LIMIT 1`, clientID, name).Scan(&id)
	if err == nil {
		return s.Get(ctx, clientID, id)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		return nil, err
	}
	return s.Register(ctx, clientID, name, ToJWK(pub))
}

// AgentName es el nombre de un agente y la integración a la que pertenece.
type AgentName struct {
	Name     string
	ClientID string
}

// Names devuelve el nombre de cada agente pedido que exista. Lo usa el titular
// para leer quién actuó sobre su cuenta, así que no filtra por integración.
func (s *Store) Names(ctx context.Context, ids []string) (map[string]AgentName, error) {
	valid := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, err := uuid.Parse(id); err == nil {
			valid = append(valid, id)
		}
	}
	out := map[string]AgentName{}
	if len(valid) == 0 {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, client_id FROM agents.agents WHERE id = ANY($1::uuid[])`, pq.Array(valid))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var n AgentName
		if err := rows.Scan(&id, &n.Name, &n.ClientID); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}
