// Package passkeys autentica a los titulares con passkeys (WebAuthn/FIDO2).
//
// Es el login real que sustituye al autenticador de juguete del sandbox. Las
// passkeys no son un extra: la Resolución SBS 2286-2024 exige dos factores
// independientes en toda operación digital y hace responsable al banco si no
// los aplicó. Una passkey con verificación de usuario los cubre en un solo
// gesto —posesión de la clave y biometría o PIN del dispositivo— sin OTP por
// SMS (doc 14).
//
// La verificación criptográfica la hace go-webauthn: no se escribe a mano. Lo que
// este paquete decide es la POLÍTICA: quién puede registrar una passkey, que un
// desafío valga una sola vez, que la verificación de usuario sea obligatoria, y
// qué hacer ante una passkey clonada.
package passkeys

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/aibank/aibank/services/onboarding"
	"github.com/aibank/aibank/services/sessions"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"
)

var (
	// ErrCeremonyNotFound: el desafío no existe, venció o ya se usó.
	ErrCeremonyNotFound = errors.New("ceremonia inexistente, vencida o ya usada")
	// ErrNotEnrollable: esa referencia de alta no habilita registrar una passkey.
	ErrNotEnrollable = errors.New("el alta no habilita registrar una passkey")
	// ErrAlreadyEnrolled: el cliente ya tiene passkey, o esa credencial ya existe.
	ErrAlreadyEnrolled = errors.New("ya hay una passkey registrada")
	// ErrVerificationFailed: la respuesta del autenticador no superó la verificación.
	ErrVerificationFailed = errors.New("la verificación de la passkey falló")
	// ErrUnknownCredential: nadie registró esa passkey en este banco.
	ErrUnknownCredential = errors.New("passkey desconocida")
	// ErrCloned: el contador de firmas retrocedió: hay dos copias de la clave.
	ErrCloned = errors.New("la passkey parece clonada")
	// ErrSessionInvalid: la sesión no existe, venció o se cerró.
	ErrSessionInvalid = sessions.ErrInvalid
)

// Config del Relying Party y de los plazos.
type Config struct {
	// RPID es el dominio al que quedan atadas las passkeys. Cambiarlo después
	// invalida todas las existentes: se elige una vez y para siempre.
	RPID          string
	RPDisplayName string
	// RPOrigins: los orígenes desde los que se aceptan ceremonias. La app de iOS
	// firma como https://<RPID>; la de Android, como android:apk-key-hash:<hash>.
	RPOrigins []string

	// SessionTTL es la vida de una sesión. Corta a propósito: volver a entrar
	// cuesta mirar el teléfono, y una sesión robada vale poco tiempo.
	SessionTTL time.Duration
	// CeremonyTTL es cuánto vive un desafío sin usarse.
	CeremonyTTL time.Duration
	// EnrollmentWindow: tras aprobarse el alta, cuánto tiempo vale su referencia
	// para registrar la primera passkey.
	EnrollmentWindow time.Duration
}

func (c *Config) defaults() {
	if c.SessionTTL == 0 {
		c.SessionTTL = 30 * time.Minute
	}
	if c.CeremonyTTL == 0 {
		c.CeremonyTTL = 5 * time.Minute
	}
	if c.EnrollmentWindow == 0 {
		c.EnrollmentWindow = 24 * time.Hour
	}
}

// Session es lo que se le entrega a la app tras una ceremonia superada.
type Session = sessions.Session

type Service struct {
	wa         *webauthn.WebAuthn
	store      *Store
	onboarding *onboarding.Store
	sessions   *sessions.Store
	cfg        Config
	now        func() time.Time
}

func NewService(cfg Config, store *Store, onboardingStore *onboarding.Store, sessionStore *sessions.Store) (*Service, error) {
	cfg.defaults()
	wa, err := webauthn.New(&webauthn.Config{
		RPID:          cfg.RPID,
		RPDisplayName: cfg.RPDisplayName,
		RPOrigins:     cfg.RPOrigins,
	})
	if err != nil {
		return nil, fmt.Errorf("configuración WebAuthn: %w", err)
	}
	return &Service{wa: wa, store: store, onboarding: onboardingStore, sessions: sessionStore, cfg: cfg, now: time.Now}, nil
}

// SetClock fija el reloj. Solo para pruebas.
func (s *Service) SetClock(now func() time.Time) { s.now = now }

// ------------------------------------------------------------------ registro

// registrationOptions: passkey de plataforma, descubrible y con verificación de
// usuario OBLIGATORIA. Sin esta última, la passkey sería un solo factor —tener
// el teléfono— y no cumpliría la norma de dos factores.
func registrationOptions(exclude []webauthn.Credential) []webauthn.RegistrationOption {
	descriptors := make([]protocol.CredentialDescriptor, 0, len(exclude))
	for _, c := range exclude {
		descriptors = append(descriptors, c.Descriptor())
	}
	return []webauthn.RegistrationOption{
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			AuthenticatorAttachment: protocol.Platform,
			ResidentKey:             protocol.ResidentKeyRequirementRequired,
			UserVerification:        protocol.VerificationRequired,
		}),
		// Sin atestación: no le pedimos al dispositivo que revele su modelo. La
		// confianza en el dispositivo es otra pieza (App Attest / Play Integrity).
		webauthn.WithConveyancePreference(protocol.PreferNoAttestation),
		// No registrar dos veces la misma passkey del mismo llavero.
		webauthn.WithExclusions(descriptors),
	}
}

// BeginEnrollment empieza el registro de la PRIMERA passkey de un cliente.
//
// La prueba de que quien pide es el titular es la referencia de su alta: la
// generó su dispositivo, solo la conocen ese dispositivo y el banco, y se acaba
// de usar para abrir la cuenta. Vale solo si el alta está aprobada, si el
// cliente todavía no tiene passkey, y dentro de la ventana tras la aprobación.
// Pasado eso, añadir un dispositivo exige una sesión iniciada.
func (s *Service) BeginEnrollment(ctx context.Context, reference string) (string, *protocol.CredentialCreation, error) {
	app, err := s.onboarding.ByExternalRef(ctx, "app:"+reference)
	if err != nil || app.Status != onboarding.StatusApproved {
		return "", nil, ErrNotEnrollable
	}
	if s.now().Sub(app.UpdatedAt) > s.cfg.EnrollmentWindow {
		return "", nil, ErrNotEnrollable
	}
	enrolled, err := s.store.hasPasskey(ctx, app.CustomerID)
	if err != nil {
		return "", nil, err
	}
	if enrolled {
		return "", nil, ErrNotEnrollable
	}

	return s.beginRegistration(ctx, kindEnrollment, app.CustomerID, app.Applicant.FullName, nil)
}

// BeginAddDevice empieza el registro de una passkey MÁS, con sesión iniciada.
func (s *Service) BeginAddDevice(ctx context.Context, customerID string) (string, *protocol.CredentialCreation, error) {
	existing, err := s.store.passkeysOf(ctx, customerID)
	if err != nil {
		return "", nil, err
	}
	if len(existing) == 0 {
		// Sin passkey previa no hay sesión de passkey que pueda haber llegado
		// hasta aquí; el primer dispositivo se registra con el alta.
		return "", nil, ErrNotEnrollable
	}
	creds := make([]webauthn.Credential, 0, len(existing))
	for _, p := range existing {
		creds = append(creds, p.credential)
	}
	return s.beginRegistration(ctx, kindAddDevice, customerID, existing[0].userName, creds)
}

func (s *Service) beginRegistration(ctx context.Context, kind ceremonyKind, customerID, userName string, existing []webauthn.Credential) (string, *protocol.CredentialCreation, error) {
	user, err := newUser(customerID, userName, existing)
	if err != nil {
		return "", nil, err
	}
	creation, session, err := s.wa.BeginRegistration(user, registrationOptions(existing)...)
	if err != nil {
		return "", nil, fmt.Errorf("iniciar registro: %w", err)
	}

	id := uuid.NewString()
	if err := s.store.saveCeremony(ctx, id, ceremony{
		kind: kind, customerID: customerID, userName: userName, session: *session,
	}, s.now().Add(s.cfg.CeremonyTTL)); err != nil {
		return "", nil, err
	}
	return id, creation, nil
}

// FinishRegistration verifica la passkey nueva, la guarda y abre sesión.
func (s *Service) FinishRegistration(ctx context.Context, ceremonyID string, response []byte) (*Session, error) {
	c, err := s.store.consumeCeremony(ctx, ceremonyID, kindEnrollment, kindAddDevice)
	if err != nil {
		return nil, err
	}

	parsed, err := protocol.ParseCredentialCreationResponseBytes(response)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrVerificationFailed, err)
	}
	existing, err := s.store.passkeysOf(ctx, c.customerID)
	if err != nil {
		return nil, err
	}
	creds := make([]webauthn.Credential, 0, len(existing))
	for _, p := range existing {
		creds = append(creds, p.credential)
	}
	user, err := newUser(c.customerID, c.userName, creds)
	if err != nil {
		return nil, err
	}

	cred, err := s.wa.CreateCredential(user, c.session, parsed)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrVerificationFailed, err)
	}
	// La librería ya lo exige porque se pidió `required`; se comprueba otra vez
	// aquí porque de esto depende que la passkey sea de dos factores.
	if !cred.Flags.UserVerified {
		return nil, fmt.Errorf("%w: el dispositivo no verificó a la persona", ErrVerificationFailed)
	}

	// El primer registro solo vale si sigue siendo el primero: dos enrolamientos
	// simultáneos con la misma referencia no pueden dejar dos passkeys.
	if err := s.store.insertPasskey(ctx, c.customerID, c.userName, cred, c.kind == kindEnrollment); err != nil {
		return nil, err
	}
	return s.openSession(ctx, c.customerID, cred.ID)
}

// --------------------------------------------------------------------- login

// BeginLogin empieza un login sin pedir quién es: la passkey lo dice. Así la
// pantalla de entrada no tiene un campo de usuario que alguien pueda probar
// para averiguar quién es cliente.
func (s *Service) BeginLogin(ctx context.Context) (string, *protocol.CredentialAssertion, error) {
	assertion, session, err := s.wa.BeginDiscoverableLogin(
		webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return "", nil, fmt.Errorf("iniciar login: %w", err)
	}
	id := uuid.NewString()
	if err := s.store.saveCeremony(ctx, id, ceremony{kind: kindLogin, session: *session},
		s.now().Add(s.cfg.CeremonyTTL)); err != nil {
		return "", nil, err
	}
	return id, assertion, nil
}

// FinishLogin verifica la firma de la passkey y abre sesión.
func (s *Service) FinishLogin(ctx context.Context, ceremonyID string, response []byte) (*Session, error) {
	c, err := s.store.consumeCeremony(ctx, ceremonyID, kindLogin)
	if err != nil {
		return nil, err
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(response)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrVerificationFailed, err)
	}

	var owner string
	handler := func(rawID, userHandle []byte) (webauthn.User, error) {
		stored, err := s.store.passkeyByCredential(ctx, rawID)
		if err != nil {
			return nil, err
		}
		// La passkey dice de quién es (userHandle) y el banco sabe de quién es
		// (lo que guardó al registrarla). Tienen que coincidir.
		if string(userHandle) != string(userID(stored.customerID)) {
			return nil, ErrUnknownCredential
		}
		owner = stored.customerID
		return newUser(stored.customerID, stored.userName, []webauthn.Credential{stored.credential})
	}

	_, cred, err := s.wa.ValidatePasskeyLogin(handler, c.session, parsed)
	if err != nil {
		if errors.Is(err, ErrUnknownCredential) {
			return nil, ErrUnknownCredential
		}
		return nil, fmt.Errorf("%w: %v", ErrVerificationFailed, err)
	}
	if !cred.Flags.UserVerified {
		return nil, fmt.Errorf("%w: el dispositivo no verificó a la persona", ErrVerificationFailed)
	}
	// Contador que retrocede: existen dos copias de la clave privada. Se niega
	// la entrada; quien tiene la copia buena puede volver a entrar cuando se
	// investigue, quien tiene la clonada no debe entrar nunca.
	if cred.Authenticator.CloneWarning {
		return nil, ErrCloned
	}
	if err := s.store.updateAfterLogin(ctx, cred); err != nil {
		return nil, err
	}
	return s.openSession(ctx, owner, cred.ID)
}

// -------------------------------------------------------------------- sesión

func (s *Service) openSession(ctx context.Context, customerID string, credentialID []byte) (*Session, error) {
	return s.sessions.Open(ctx, customerID, sessions.FactorPasskey,
		base64.RawURLEncoding.EncodeToString(credentialID), s.now().Add(s.cfg.SessionTTL))
}

// ResolveSession devuelve el cliente y lo que firmó detrás de un token.
func (s *Service) ResolveSession(ctx context.Context, token string) (customerID, deviceRef string, err error) {
	p, err := s.sessions.Resolve(ctx, token)
	if err != nil {
		return "", "", err
	}
	return p.CustomerID, p.DeviceRef, nil
}

// Logout cierra la sesión. Cerrar una que ya no existe no es un error.
func (s *Service) Logout(ctx context.Context, token string) error {
	return s.sessions.Revoke(ctx, token)
}

// ---------------------------------------------------------------- usuario

// userID es el identificador que queda guardado en la passkey: los 16 bytes del
// UUID del cliente. Nada personal —ni DNI ni nombre—: el estándar advierte que
// el authenticator puede mostrarlo o sincronizarlo.
func userID(customerID string) []byte {
	id, err := uuid.Parse(customerID)
	if err != nil {
		return nil
	}
	return id[:]
}

type user struct {
	id    []byte
	name  string
	creds []webauthn.Credential
}

func newUser(customerID, name string, creds []webauthn.Credential) (*user, error) {
	id := userID(customerID)
	if id == nil {
		return nil, fmt.Errorf("customer_id inválido: %q", customerID)
	}
	return &user{id: id, name: name, creds: creds}, nil
}

func (u *user) WebAuthnID() []byte                         { return u.id }
func (u *user) WebAuthnName() string                       { return u.name }
func (u *user) WebAuthnDisplayName() string                { return u.name }
func (u *user) WebAuthnCredentials() []webauthn.Credential { return u.creds }
