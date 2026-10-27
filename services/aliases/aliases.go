// Package aliases es el directorio de celulares: enviar dinero a un número en
// vez de a una cuenta, como en Yape o Plin.
//
// Registrar un número exige probar que se tiene: un código por SMS. Buscar un
// número devuelve su cuenta y un nombre a medias ("Juan P."), lo justo para que
// quien envía reconozca a quién, sin que el directorio sirva para averiguar
// nombres completos.
package aliases

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

//go:embed schema.sql
var schema string

var (
	// ErrInvalidPhone: no es un celular peruano.
	ErrInvalidPhone = errors.New("número de celular inválido")
	// ErrNotFound: ese número no está en el directorio.
	ErrNotFound = errors.New("número no afiliado")
	// ErrVerificationNotFound: el código venció, ya se usó o es de otro.
	ErrVerificationNotFound = errors.New("verificación inexistente o vencida")
	// ErrTooManyAttempts: se agotaron los intentos; hay que pedir otro código.
	ErrTooManyAttempts = errors.New("demasiados intentos")
)

// WrongCodeError: el código no coincide. Lleva cuántos intentos quedan.
type WrongCodeError struct{ Remaining int }

func (e *WrongCodeError) Error() string {
	return fmt.Sprintf("código incorrecto, quedan %d intentos", e.Remaining)
}

// Sender manda el código por SMS. Es un puerto: el proveedor real se elige
// aparte; el sandbox usa SandboxSender.
type Sender interface {
	SendCode(ctx context.Context, phone, code string) error
}

const (
	codeTTL     = 10 * time.Minute
	maxAttempts = 5
)

type Service struct {
	db     *sql.DB
	sender Sender
	now    func() time.Time
}

func NewService(db *sql.DB, sender Sender) *Service {
	return &Service{db: db, sender: sender, now: time.Now}
}

func (s *Service) Migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, schema)
	return err
}

// NormalizePhone acepta "987 654 321", "987-654-321" o "+51987654321" y
// devuelve "+51987654321". Solo celulares: nueve dígitos que empiezan por 9.
func NormalizePhone(input string) (string, error) {
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, input)
	if len(digits) == 11 && strings.HasPrefix(digits, "51") {
		digits = digits[2:]
	}
	if len(digits) != 9 || digits[0] != '9' {
		return "", ErrInvalidPhone
	}
	return "+51" + digits, nil
}

// Verification es un código enviado.
type Verification struct {
	ID        string
	Phone     string
	ExpiresAt time.Time
}

// StartVerification envía un código al número para asociarlo a la cuenta.
func (s *Service) StartVerification(ctx context.Context, customerID, accountID, input string) (*Verification, error) {
	phone, err := NormalizePhone(input)
	if err != nil {
		return nil, err
	}
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return nil, err
	}
	code := fmt.Sprintf("%06d", n.Int64())
	v := &Verification{ID: uuid.NewString(), Phone: phone, ExpiresAt: s.now().Add(codeTTL)}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO identity.phone_verifications (id, customer_id, account_id, phone, code_hash, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		v.ID, customerID, accountID, phone, hashCode(v.ID, code), v.ExpiresAt); err != nil {
		return nil, err
	}
	if err := s.sender.SendCode(ctx, phone, code); err != nil {
		return nil, fmt.Errorf("enviar el código: %w", err)
	}
	return v, nil
}

// Verify comprueba el código y, si coincide, deja el número a nombre del cliente.
func (s *Service) Verify(ctx context.Context, customerID, verificationID, code string) (string, error) {
	if _, err := uuid.Parse(verificationID); err != nil {
		return "", ErrVerificationNotFound
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()

	var (
		accountID, phone string
		stored           []byte
		attempts         int
	)
	// FOR UPDATE: dos intentos simultáneos no pueden contar como uno.
	err = tx.QueryRowContext(ctx, `
		SELECT account_id, phone, code_hash, attempts FROM identity.phone_verifications
		 WHERE id = $1 AND customer_id = $2 AND consumed_at IS NULL AND expires_at > now()
		 FOR UPDATE`, verificationID, customerID).Scan(&accountID, &phone, &stored, &attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrVerificationNotFound
	}
	if err != nil {
		return "", err
	}
	if attempts >= maxAttempts {
		return "", ErrTooManyAttempts
	}

	if subtle.ConstantTimeCompare(hashCode(verificationID, code), stored) != 1 {
		if _, err := tx.ExecContext(ctx, `
			UPDATE identity.phone_verifications SET attempts = attempts + 1 WHERE id = $1`, verificationID); err != nil {
			return "", err
		}
		if err := tx.Commit(); err != nil {
			return "", err
		}
		if attempts+1 >= maxAttempts {
			return "", ErrTooManyAttempts
		}
		return "", &WrongCodeError{Remaining: maxAttempts - attempts - 1}
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE identity.phone_verifications SET consumed_at = now() WHERE id = $1`, verificationID); err != nil {
		return "", err
	}
	if err := register(ctx, tx, customerID, accountID, phone); err != nil {
		return "", err
	}
	return phone, tx.Commit()
}

// RegisterVerified deja un número a nombre de un cliente sin mandar código.
//
// Solo para números cuya posesión ya se comprobó por otro lado. El sandbox lo
// usa para darle un número al destinatario de prueba, que no tiene teléfono.
func (s *Service) RegisterVerified(ctx context.Context, customerID, accountID, input string) (string, error) {
	phone, err := NormalizePhone(input)
	if err != nil {
		return "", err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	if err := register(ctx, tx, customerID, accountID, phone); err != nil {
		return "", err
	}
	return phone, tx.Commit()
}

// register: el cliente se queda con un solo número —el nuevo reemplaza al que
// tuviera— y el número pasa a él aunque fuera de otro.
func register(ctx context.Context, tx *sql.Tx, customerID, accountID, phone string) error {
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM identity.phone_aliases WHERE customer_id = $1 AND phone <> $2`, customerID, phone); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO identity.phone_aliases (phone, customer_id, account_id) VALUES ($1, $2, $3)
		ON CONFLICT (phone) DO UPDATE
		   SET customer_id = EXCLUDED.customer_id, account_id = EXCLUDED.account_id, verified_at = now()`,
		phone, customerID, accountID)
	return err
}

// Alias es el número de un cliente y la cuenta a la que llega.
type Alias struct {
	Phone     string
	AccountID string
}

// Mine devuelve el número del cliente.
func (s *Service) Mine(ctx context.Context, customerID string) (*Alias, error) {
	var a Alias
	err := s.db.QueryRowContext(ctx, `
		SELECT phone, account_id FROM identity.phone_aliases WHERE customer_id = $1`, customerID).
		Scan(&a.Phone, &a.AccountID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &a, err
}

// Remove saca el número del cliente del directorio.
func (s *Service) Remove(ctx context.Context, customerID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM identity.phone_aliases WHERE customer_id = $1`, customerID)
	return err
}

// Resolve devuelve la cuenta a la que llega lo que se envía al número.
func (s *Service) Resolve(ctx context.Context, input string) (*Alias, error) {
	phone, err := NormalizePhone(input)
	if err != nil {
		return nil, err
	}
	var a Alias
	err = s.db.QueryRowContext(ctx, `
		SELECT phone, account_id FROM identity.phone_aliases WHERE phone = $1`, phone).
		Scan(&a.Phone, &a.AccountID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &a, err
}

// MaskName deja el primer nombre y la inicial del primer apellido: "Carmen
// Huaman Díaz" → "Carmen H.". Basta para reconocer a alguien y no para
// averiguar su nombre.
//
// En Perú se llevan dos apellidos y el que se usa es el primero: con tres
// palabras o más se toma la penúltima. Es una heurística —"Ana María Torres"
// daría "Ana M."—; lo correcto será guardar nombres y apellidos por separado.
func MaskName(name string) string {
	if i := strings.Index(name, "("); i >= 0 {
		name = name[:i]
	}
	words := strings.Fields(name)
	switch len(words) {
	case 0:
		return ""
	case 1:
		return words[0]
	case 2:
		return words[0] + " " + string([]rune(words[1])[0]) + "."
	}
	return words[0] + " " + string([]rune(words[len(words)-2])[0]) + "."
}

func hashCode(id, code string) []byte {
	sum := sha256.Sum256([]byte(id + ":" + code))
	return sum[:]
}

// SandboxSender no manda SMS: guarda el último código de cada número para que
// el sandbox lo muestre. Nunca en producción.
type SandboxSender struct {
	mu    sync.Mutex
	codes map[string]string
}

func NewSandboxSender() *SandboxSender { return &SandboxSender{codes: map[string]string{}} }

func (s *SandboxSender) SendCode(_ context.Context, phone, code string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.codes[phone] = code
	return nil
}

// LastCode es el último código "enviado" a un número.
func (s *SandboxSender) LastCode(phone string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.codes[phone]
}
