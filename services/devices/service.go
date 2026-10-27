// Package devices vincula teléfonos a un titular y los deja entrar.
//
// Es la capa diaria del login: la persona abre la app, mira la cámara y está
// dentro. Debajo, el teléfono firma un desafío del banco con una llave que vive
// en su chip seguro y que solo se usa tras la biometría. A diferencia de una
// passkey, esa llave no se copia a ningún otro equipo: el banco sabe qué
// teléfono firmó (doc 14, roadmap 5c).
//
// Si la biometría falla, entra la clave de 6 dígitos: se verifica en el banco,
// con bloqueo tras cinco intentos, y solo junto con la firma de la otra llave
// del teléfono, para que nadie pueda probar claves desde fuera de él.
package devices

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/aibank/aibank/services/onboarding"
	"github.com/aibank/aibank/services/sessions"
	"github.com/google/uuid"
	"golang.org/x/crypto/argon2"
)

var (
	// ErrChallengeNotFound: el desafío no existe, venció o ya se usó.
	ErrChallengeNotFound = errors.New("desafío inexistente, vencido o ya usado")
	// ErrNotEnrollable: no se puede vincular este teléfono con lo presentado.
	ErrNotEnrollable = errors.New("no se puede vincular el dispositivo")
	// ErrInvalidKey: una clave pública que no es P-256, o las dos iguales.
	ErrInvalidKey = errors.New("clave pública inválida")
	// ErrWeakPIN: la clave no tiene 6 dígitos o es de las que se adivinan.
	ErrWeakPIN = errors.New("clave débil")
	// ErrRejected: dispositivo desconocido o desvinculado, o firma que no verifica.
	ErrRejected = errors.New("dispositivo rechazado")
	// ErrLocked: demasiadas claves incorrectas. Se desbloquea entrando con la
	// passkey o por soporte, nunca esperando.
	ErrLocked = errors.New("dispositivo bloqueado")
)

// WrongPINError: la clave no coincide. Lleva cuántos intentos quedan.
type WrongPINError struct{ Remaining int }

func (e *WrongPINError) Error() string {
	return fmt.Sprintf("clave incorrecta, quedan %d intentos", e.Remaining)
}

type Config struct {
	SessionTTL       time.Duration
	ChallengeTTL     time.Duration
	EnrollmentWindow time.Duration
	// MaxPINAttempts: intentos fallidos seguidos antes de bloquear el teléfono.
	MaxPINAttempts int
}

func (c *Config) defaults() {
	if c.SessionTTL == 0 {
		c.SessionTTL = 30 * time.Minute
	}
	if c.ChallengeTTL == 0 {
		c.ChallengeTTL = 2 * time.Minute
	}
	if c.EnrollmentWindow == 0 {
		c.EnrollmentWindow = 24 * time.Hour
	}
	if c.MaxPINAttempts == 0 {
		c.MaxPINAttempts = 5
	}
}

type Service struct {
	store      *Store
	onboarding *onboarding.Store
	sessions   *sessions.Store
	cfg        Config
	now        func() time.Time
}

func NewService(cfg Config, store *Store, onboardingStore *onboarding.Store, sessionStore *sessions.Store) *Service {
	cfg.defaults()
	return &Service{store: store, onboarding: onboardingStore, sessions: sessionStore, cfg: cfg, now: time.Now}
}

// Challenge es lo que el teléfono tiene que firmar, tal cual.
type Challenge struct {
	ID      string
	Payload []byte
}

// Registration es lo que manda el teléfono para vincularse.
type Registration struct {
	ChallengeID string
	// SubjectPublicKeyInfo en DER, P-256.
	BiometricKey  []byte
	PossessionKey []byte
	// Firmas ECDSA (DER) del desafío con cada llave: prueban que el teléfono
	// tiene las privadas, y que la biometría funciona antes de depender de ella.
	BiometricSignature  []byte
	PossessionSignature []byte
	PIN                 string
	Name                string
	HardwareBacked      bool
}

// Result de vincular: el id que el teléfono guarda y la sesión ya abierta.
type Result struct {
	DeviceID string
	Session  *sessions.Session
}

// ------------------------------------------------------------- vincular

func (s *Service) BeginRegistration(ctx context.Context) (*Challenge, error) {
	return s.challenge(ctx, purposeRegister, "")
}

// EnrollWithReference vincula el PRIMER teléfono de un cliente recién aprobado.
//
// La prueba de que es el titular es la referencia de su alta, como para la
// primera passkey: la generó su teléfono y solo la conocen él y el banco. Vale
// con el alta aprobada, dentro de la ventana, y si el cliente nunca vinculó
// otro teléfono. Después, vincular otro exige una sesión.
func (s *Service) EnrollWithReference(ctx context.Context, reference string, r Registration) (*Result, error) {
	app, err := s.onboarding.ByExternalRef(ctx, "app:"+reference)
	if err != nil || app.Status != onboarding.StatusApproved || s.now().Sub(app.UpdatedAt) > s.cfg.EnrollmentWindow {
		return nil, ErrNotEnrollable
	}
	return s.register(ctx, app.CustomerID, r, true)
}

// AddDevice vincula un teléfono más a un cliente con sesión iniciada —por
// ejemplo, tras entrar con su passkey en un teléfono nuevo—.
func (s *Service) AddDevice(ctx context.Context, customerID string, r Registration) (*Result, error) {
	return s.register(ctx, customerID, r, false)
}

func (s *Service) register(ctx context.Context, customerID string, r Registration, onlyIfFirst bool) (*Result, error) {
	// La clave se valida antes de gastar el desafío: un error de tipeo no debe
	// obligar a repetir la biometría.
	if err := checkPIN(r.PIN); err != nil {
		return nil, err
	}
	bio, err := parseKey(r.BiometricKey)
	if err != nil {
		return nil, err
	}
	possession, err := parseKey(r.PossessionKey)
	if err != nil {
		return nil, err
	}
	if bytes.Equal(r.BiometricKey, r.PossessionKey) {
		return nil, fmt.Errorf("%w: las dos llaves son la misma", ErrInvalidKey)
	}

	payload, _, err := s.store.consumeChallenge(ctx, r.ChallengeID, purposeRegister)
	if err != nil {
		return nil, err
	}
	if !verify(bio, payload, r.BiometricSignature) || !verify(possession, payload, r.PossessionSignature) {
		return nil, ErrRejected
	}

	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	name := r.Name
	if name == "" {
		name = "Teléfono"
	}
	id := uuid.NewString()
	if err := s.store.insertDevice(ctx, newDevice{
		id: id, customerID: customerID, name: name,
		biometricKey: r.BiometricKey, possessionKey: r.PossessionKey,
		pinHash: hashPIN(r.PIN, salt), pinSalt: salt, hardwareBacked: r.HardwareBacked,
	}, onlyIfFirst); err != nil {
		return nil, err
	}

	// Vincular cuenta como entrar con biometría: se acaba de firmar con esa llave.
	session, err := s.sessions.Open(ctx, customerID, sessions.FactorDeviceBiometric, id, s.now().Add(s.cfg.SessionTTL))
	if err != nil {
		return nil, err
	}
	return &Result{DeviceID: id, Session: session}, nil
}

// ----------------------------------------------------------------- entrar

// BeginLogin emite el desafío para un teléfono vinculado.
func (s *Service) BeginLogin(ctx context.Context, deviceID string) (*Challenge, error) {
	if _, err := uuid.Parse(deviceID); err != nil {
		return nil, ErrRejected
	}
	d, err := s.store.deviceByID(ctx, deviceID)
	if err != nil {
		return nil, err
	}
	if err := usable(d); err != nil {
		return nil, err
	}
	return s.challenge(ctx, purposeLogin, deviceID)
}

// LoginWithBiometrics: firma con la llave que solo se usa tras la biometría.
func (s *Service) LoginWithBiometrics(ctx context.Context, challengeID string, signature []byte) (*sessions.Session, error) {
	d, payload, err := s.loginDevice(ctx, challengeID)
	if err != nil {
		return nil, err
	}
	key, err := parseKey(d.biometricKey)
	if err != nil || !verify(key, payload, signature) {
		return nil, ErrRejected
	}
	return s.enter(ctx, d, sessions.FactorDeviceBiometric)
}

// LoginWithPIN: firma con la llave de posesión, más la clave de 6 dígitos.
//
// La firma se comprueba ANTES que la clave: sin ella, un intento no cuenta ni
// llega a compararse. Así solo se pueden probar claves desde el teléfono, y
// desde él el tope de intentos lo frena.
func (s *Service) LoginWithPIN(ctx context.Context, challengeID string, signature []byte, pin string) (*sessions.Session, error) {
	d, payload, err := s.loginDevice(ctx, challengeID)
	if err != nil {
		return nil, err
	}
	key, err := parseKey(d.possessionKey)
	if err != nil || !verify(key, payload, signature) {
		return nil, ErrRejected
	}
	if subtle.ConstantTimeCompare(hashPIN(pin, d.pinSalt), d.pinHash) != 1 {
		attempts, err := s.store.failPIN(ctx, d.id, s.cfg.MaxPINAttempts)
		if err != nil {
			return nil, err
		}
		if attempts >= s.cfg.MaxPINAttempts {
			return nil, ErrLocked
		}
		return nil, &WrongPINError{Remaining: s.cfg.MaxPINAttempts - attempts}
	}
	return s.enter(ctx, d, sessions.FactorDevicePIN)
}

func (s *Service) loginDevice(ctx context.Context, challengeID string) (*device, []byte, error) {
	payload, deviceID, err := s.store.consumeChallenge(ctx, challengeID, purposeLogin)
	if err != nil {
		return nil, nil, err
	}
	d, err := s.store.deviceByID(ctx, deviceID)
	if err != nil {
		return nil, nil, err
	}
	// Se comprueba otra vez: pudo bloquearse o desvincularse entre el desafío y
	// la firma.
	if err := usable(d); err != nil {
		return nil, nil, err
	}
	return d, payload, nil
}

func (s *Service) enter(ctx context.Context, d *device, factor sessions.Factor) (*sessions.Session, error) {
	if err := s.store.markUsed(ctx, d.id); err != nil {
		return nil, err
	}
	return s.sessions.Open(ctx, d.customerID, factor, d.id, s.now().Add(s.cfg.SessionTTL))
}

// Revoke desvincula un teléfono del cliente y cierra lo que tenga abierto.
func (s *Service) Revoke(ctx context.Context, customerID, deviceID string) error {
	if _, err := uuid.Parse(deviceID); err != nil {
		return ErrRejected
	}
	if err := s.store.revoke(ctx, customerID, deviceID); err != nil {
		return err
	}
	return s.sessions.RevokeDevice(ctx, deviceID)
}

// -------------------------------------------------------------- utilidades

// payloadPrefix separa lo que firma esta llave de cualquier otra cosa que
// alguien pudiera pedirle firmar: una firma de login no vale como otra cosa.
const payloadPrefix = "aerarium/device/v1"

func (s *Service) challenge(ctx context.Context, p purpose, deviceID string) (*Challenge, error) {
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	id := uuid.NewString()
	payload := []byte(payloadPrefix + "\n" + string(p) + "\n" + id + "\n" +
		base64.RawURLEncoding.EncodeToString(nonce))
	if err := s.store.saveChallenge(ctx, id, p, deviceID, payload, s.now().Add(s.cfg.ChallengeTTL)); err != nil {
		return nil, err
	}
	return &Challenge{ID: id, Payload: payload}, nil
}

func usable(d *device) error {
	if d.revoked {
		return ErrRejected
	}
	if d.locked {
		return ErrLocked
	}
	return nil
}

func parseKey(der []byte) (*ecdsa.PublicKey, error) {
	parsed, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidKey, err)
	}
	key, ok := parsed.(*ecdsa.PublicKey)
	if !ok || key.Curve != elliptic.P256() {
		return nil, fmt.Errorf("%w: se esperaba P-256", ErrInvalidKey)
	}
	return key, nil
}

func verify(key *ecdsa.PublicKey, payload, signature []byte) bool {
	digest := sha256.Sum256(payload)
	return ecdsa.VerifyASN1(key, digest[:], signature)
}

func hashPIN(pin string, salt []byte) []byte {
	return argon2.IDKey([]byte(pin), salt, 1, 64*1024, 4, 32)
}

// checkPIN: seis dígitos, y ninguna de las claves que se prueban primero.
func checkPIN(pin string) error {
	if len(pin) != 6 {
		return ErrWeakPIN
	}
	for _, c := range pin {
		if c < '0' || c > '9' {
			return ErrWeakPIN
		}
	}
	same, up, down := true, true, true
	for i := 1; i < len(pin); i++ {
		same = same && pin[i] == pin[0]
		up = up && pin[i] == pin[i-1]+1
		down = down && pin[i] == pin[i-1]-1
	}
	if same || up || down {
		return ErrWeakPIN
	}
	return nil
}
