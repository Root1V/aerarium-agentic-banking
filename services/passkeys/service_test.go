package passkeys_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"testing"
	"time"

	"github.com/aibank/aibank/adapters/kyc"
	"github.com/aibank/aibank/services/onboarding"
	"github.com/aibank/aibank/services/passkeys"
	"github.com/aibank/aibank/services/sessions"
	"github.com/descope/virtualwebauthn"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
)

// Las ceremonias se ejercitan con un autenticador virtual que FIRMA DE VERDAD:
// genera su par de claves, construye los datos del autenticador y firma el
// desafío como lo haría el chip seguro de un teléfono. La verificación del
// servidor no sabe que es una prueba.

const rpID = "aerarium.test"

var rp = virtualwebauthn.RelyingParty{ID: rpID, Name: "AIBank", Origin: "https://" + rpID}

type env struct {
	ctx context.Context
	svc *passkeys.Service
	onb *onboarding.Store
}

func setup(t *testing.T) *env { return setupWith(t, nil) }

func setupWith(t *testing.T, tweak func(*passkeys.Config)) *env {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		url = "postgres://aibank:aibank_dev@localhost:5434/aibank?sslmode=disable"
	}
	db, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	store := passkeys.NewStore(db)
	if err := store.Migrate(ctx); err != nil {
		t.Skipf("base no disponible: %v", err)
	}
	onb := onboarding.NewStore(db)
	if err := onb.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	cfg := passkeys.Config{
		RPID:          rpID,
		RPDisplayName: "AIBank",
		RPOrigins:     []string{"https://" + rpID},
	}
	if tweak != nil {
		tweak(&cfg)
	}
	sess := sessions.NewStore(db)
	if err := sess.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	svc, err := passkeys.NewService(cfg, store, onb, sess)
	if err != nil {
		t.Fatal(err)
	}
	return &env{ctx: ctx, svc: svc, onb: onb}
}

// approved crea un alta aprobada y devuelve su referencia y el cliente.
func (e *env) approved(t *testing.T) (string, string) {
	return e.application(t, onboarding.StatusApproved)
}

func (e *env) application(t *testing.T, status onboarding.Status) (string, string) {
	t.Helper()
	reference, customer := uuid.NewString(), uuid.NewString()
	app, err := e.onb.Create(e.ctx, &onboarding.Application{
		ID: uuid.NewString(), ExternalRef: "app:" + reference, CustomerID: customer,
		ProductCode: "BASICA-USD", Status: onboarding.StatusInProgress,
		Applicant: kyc.Applicant{
			FullName: "Ana Pérez", DocumentType: "DNI",
			DocumentNumber: fmt.Sprintf("%08d", rand.Intn(100_000_000)),
			DateOfBirth:    time.Date(1995, 3, 14, 0, 0, 0, 0, time.UTC), CountryCode: "PE",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if status != onboarding.StatusInProgress {
		if err := e.onb.UpdateStatus(e.ctx, app.ID, status, ""); err != nil {
			t.Fatal(err)
		}
	}
	return reference, customer
}

// device es un teléfono con su autenticador de plataforma.
type device struct {
	auth virtualwebauthn.Authenticator
	cred virtualwebauthn.Credential
	rp   virtualwebauthn.RelyingParty
}

func newDevice(customerID string, opts virtualwebauthn.AuthenticatorOptions) *device {
	id := uuid.MustParse(customerID)
	opts.UserHandle = id[:]
	return &device{
		auth: virtualwebauthn.NewAuthenticatorWithOptions(opts),
		cred: virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2),
		rp:   rp,
	}
}

func (d *device) attest(t *testing.T, options any) []byte {
	t.Helper()
	raw, _ := json.Marshal(options)
	parsed, err := virtualwebauthn.ParseAttestationOptions(string(raw))
	if err != nil {
		t.Fatalf("opciones de registro: %v", err)
	}
	return []byte(virtualwebauthn.CreateAttestationResponse(d.rp, d.auth, d.cred, *parsed))
}

func (d *device) assert(t *testing.T, options any) []byte {
	t.Helper()
	raw, _ := json.Marshal(options)
	parsed, err := virtualwebauthn.ParseAssertionOptions(string(raw))
	if err != nil {
		t.Fatalf("opciones de login: %v", err)
	}
	d.auth.AddCredential(d.cred)
	return []byte(virtualwebauthn.CreateAssertionResponse(d.rp, d.auth, d.cred, *parsed))
}

func (e *env) enroll(t *testing.T, reference string, d *device) *passkeys.Session {
	t.Helper()
	id, options, err := e.svc.BeginEnrollment(e.ctx, reference)
	if err != nil {
		t.Fatalf("empezar registro: %v", err)
	}
	session, err := e.svc.FinishRegistration(e.ctx, id, d.attest(t, options))
	if err != nil {
		t.Fatalf("terminar registro: %v", err)
	}
	return session
}

func (e *env) login(t *testing.T, d *device) (*passkeys.Session, error) {
	t.Helper()
	id, options, err := e.svc.BeginLogin(e.ctx)
	if err != nil {
		t.Fatalf("empezar login: %v", err)
	}
	return e.svc.FinishLogin(e.ctx, id, d.assert(t, options))
}

// ---------------------------------------------------------------- camino feliz

func TestUnClienteRecienAprobadoRegistraSuPasskeyYEntra(t *testing.T) {
	e := setup(t)
	reference, customer := e.approved(t)
	phone := newDevice(customer, virtualwebauthn.AuthenticatorOptions{})

	first := e.enroll(t, reference, phone)
	if first.CustomerID != customer {
		t.Fatalf("la sesión es de %s, no de %s", first.CustomerID, customer)
	}

	// Días después, sin decir quién es: la passkey lo dice.
	phone.cred.Counter = 1
	session, err := e.login(t, phone)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	got, device, err := e.svc.ResolveSession(e.ctx, session.Token)
	if err != nil || got != customer || device == "" {
		t.Fatalf("la sesión resuelve a %q/%q (%v)", got, device, err)
	}
}

// ---------------------------------------------------------------- lo que tiene que impedir

// Una respuesta capturada en tránsito no se puede reenviar: el desafío ya se usó.
func TestUnDesafioSoloSirveUnaVez(t *testing.T) {
	e := setup(t)
	reference, customer := e.approved(t)
	phone := newDevice(customer, virtualwebauthn.AuthenticatorOptions{})
	e.enroll(t, reference, phone)

	phone.cred.Counter = 1
	id, options, _ := e.svc.BeginLogin(e.ctx)
	response := phone.assert(t, options)
	if _, err := e.svc.FinishLogin(e.ctx, id, response); err != nil {
		t.Fatalf("primer uso: %v", err)
	}
	if _, err := e.svc.FinishLogin(e.ctx, id, response); !errors.Is(err, passkeys.ErrCeremonyNotFound) {
		t.Errorf("reenvío = %v, se esperaba ErrCeremonyNotFound", err)
	}
}

// Sin verificación de usuario la passkey es un solo factor —tener el teléfono—,
// y la SBS exige dos.
func TestSinVerificacionBiometricaNoHayPasskey(t *testing.T) {
	e := setup(t)
	reference, customer := e.approved(t)
	phone := newDevice(customer, virtualwebauthn.AuthenticatorOptions{UserNotVerified: true})

	id, options, err := e.svc.BeginEnrollment(e.ctx, reference)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.FinishRegistration(e.ctx, id, phone.attest(t, options)); !errors.Is(err, passkeys.ErrVerificationFailed) {
		t.Errorf("registro sin verificación = %v, se esperaba ErrVerificationFailed", err)
	}
}

func TestSinVerificacionBiometricaNoSeEntra(t *testing.T) {
	e := setup(t)
	reference, customer := e.approved(t)
	phone := newDevice(customer, virtualwebauthn.AuthenticatorOptions{})
	e.enroll(t, reference, phone)

	phone.auth.Options.UserNotVerified = true
	phone.cred.Counter = 1
	if _, err := e.login(t, phone); !errors.Is(err, passkeys.ErrVerificationFailed) {
		t.Errorf("login sin verificación = %v, se esperaba ErrVerificationFailed", err)
	}
}

// Una página de phishing en otro dominio no consigue una firma válida: el
// origen va dentro de lo firmado, y no es el del banco.
func TestUnaPaginaDePhishingNoConsigueEntrar(t *testing.T) {
	e := setup(t)
	reference, customer := e.approved(t)
	phone := newDevice(customer, virtualwebauthn.AuthenticatorOptions{})
	e.enroll(t, reference, phone)

	phone.rp.Origin = "https://aerarium-seguro.test"
	phone.cred.Counter = 1
	if _, err := e.login(t, phone); !errors.Is(err, passkeys.ErrVerificationFailed) {
		t.Errorf("login desde otro origen = %v, se esperaba ErrVerificationFailed", err)
	}
}

func TestUnAltaQueNoEstaAprobadaNoHabilitaRegistrar(t *testing.T) {
	e := setup(t)
	enRevision, _ := e.application(t, onboarding.StatusManualReview)

	for nombre, reference := range map[string]string{
		"en revisión": enRevision,
		"inexistente": uuid.NewString(),
	} {
		if _, _, err := e.svc.BeginEnrollment(e.ctx, reference); !errors.Is(err, passkeys.ErrNotEnrollable) {
			t.Errorf("%s: %v, se esperaba ErrNotEnrollable", nombre, err)
		}
	}
}

// La referencia del alta sirve para la PRIMERA passkey. Después, quien la
// tuviera no puede colgar su propio dispositivo de la cuenta de otro.
func TestLaReferenciaDelAltaSoloSirveParaLaPrimeraPasskey(t *testing.T) {
	e := setup(t)
	reference, customer := e.approved(t)
	e.enroll(t, reference, newDevice(customer, virtualwebauthn.AuthenticatorOptions{}))

	if _, _, err := e.svc.BeginEnrollment(e.ctx, reference); !errors.Is(err, passkeys.ErrNotEnrollable) {
		t.Errorf("segundo uso de la referencia = %v, se esperaba ErrNotEnrollable", err)
	}
}

func TestLaReferenciaDelAltaVence(t *testing.T) {
	e := setup(t)
	reference, _ := e.approved(t)

	e.svc.SetClock(func() time.Time { return time.Now().Add(25 * time.Hour) })
	if _, _, err := e.svc.BeginEnrollment(e.ctx, reference); !errors.Is(err, passkeys.ErrNotEnrollable) {
		t.Errorf("pasadas 25 h = %v, se esperaba ErrNotEnrollable", err)
	}
}

// Dos registros lanzados a la vez con la misma referencia no pueden dejar dos
// passkeys: la segunda ya ve la primera.
func TestDosRegistrosSimultaneosDejanUnaSolaPasskey(t *testing.T) {
	e := setup(t)
	reference, customer := e.approved(t)
	a := newDevice(customer, virtualwebauthn.AuthenticatorOptions{})
	b := newDevice(customer, virtualwebauthn.AuthenticatorOptions{})

	idA, optsA, _ := e.svc.BeginEnrollment(e.ctx, reference)
	idB, optsB, err := e.svc.BeginEnrollment(e.ctx, reference)
	if err != nil {
		t.Fatalf("las dos empiezan antes de que ninguna termine: %v", err)
	}

	if _, err := e.svc.FinishRegistration(e.ctx, idA, a.attest(t, optsA)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.FinishRegistration(e.ctx, idB, b.attest(t, optsB)); !errors.Is(err, passkeys.ErrAlreadyEnrolled) {
		t.Errorf("segundo registro = %v, se esperaba ErrAlreadyEnrolled", err)
	}
}

// Si el contador de firmas retrocede, existen dos copias de la clave privada.
func TestUnaPasskeyClonadaNoEntra(t *testing.T) {
	e := setup(t)
	reference, customer := e.approved(t)
	phone := newDevice(customer, virtualwebauthn.AuthenticatorOptions{})
	e.enroll(t, reference, phone)

	phone.cred.Counter = 5
	if _, err := e.login(t, phone); err != nil {
		t.Fatalf("login legítimo: %v", err)
	}

	clone := *phone
	clone.cred.Counter = 3
	if _, err := e.login(t, &clone); !errors.Is(err, passkeys.ErrCloned) {
		t.Errorf("login con contador menor = %v, se esperaba ErrCloned", err)
	}
}

func TestUnaPasskeyQueElBancoNoConoceNoEntra(t *testing.T) {
	e := setup(t)
	stranger := newDevice(uuid.NewString(), virtualwebauthn.AuthenticatorOptions{})

	if _, err := e.login(t, stranger); !errors.Is(err, passkeys.ErrUnknownCredential) {
		t.Errorf("login con passkey desconocida = %v, se esperaba ErrUnknownCredential", err)
	}
}

// Un desafío emitido para entrar no sirve para registrar una passkey.
func TestUnDesafioDeLoginNoSirveParaRegistrar(t *testing.T) {
	e := setup(t)
	_, customer := e.approved(t)
	phone := newDevice(customer, virtualwebauthn.AuthenticatorOptions{})

	loginID, _, _ := e.svc.BeginLogin(e.ctx)
	_, regOptions, _ := e.svc.BeginAddDevice(e.ctx, customer) // sin passkey previa: error, opciones nil
	if regOptions != nil {
		t.Fatal("sin passkey previa no se puede añadir un dispositivo")
	}
	// Se intenta colar una respuesta de registro por la ceremonia de login.
	reference, _ := e.approved(t)
	_, options, _ := e.svc.BeginEnrollment(e.ctx, reference)
	if _, err := e.svc.FinishRegistration(e.ctx, loginID, phone.attest(t, options)); !errors.Is(err, passkeys.ErrCeremonyNotFound) {
		t.Errorf("registro por una ceremonia de login = %v, se esperaba ErrCeremonyNotFound", err)
	}
}

// Un segundo dispositivo se añade con sesión iniciada, y entra por su cuenta.
func TestOtroDispositivoSeAnadeConSesionYEntra(t *testing.T) {
	e := setup(t)
	reference, customer := e.approved(t)
	e.enroll(t, reference, newDevice(customer, virtualwebauthn.AuthenticatorOptions{}))

	tablet := newDevice(customer, virtualwebauthn.AuthenticatorOptions{})
	id, options, err := e.svc.BeginAddDevice(e.ctx, customer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.FinishRegistration(e.ctx, id, tablet.attest(t, options)); err != nil {
		t.Fatalf("añadir dispositivo: %v", err)
	}

	tablet.cred.Counter = 1
	session, err := e.login(t, tablet)
	if err != nil || session.CustomerID != customer {
		t.Fatalf("login desde el segundo dispositivo: %v", err)
	}
}

func TestUnaSesionCerradaNoSirve(t *testing.T) {
	e := setup(t)
	reference, customer := e.approved(t)
	session := e.enroll(t, reference, newDevice(customer, virtualwebauthn.AuthenticatorOptions{}))

	if err := e.svc.Logout(e.ctx, session.Token); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.ResolveSession(e.ctx, session.Token); !errors.Is(err, passkeys.ErrSessionInvalid) {
		t.Errorf("sesión cerrada = %v, se esperaba ErrSessionInvalid", err)
	}
}

func TestUnaSesionVencidaNoSirve(t *testing.T) {
	// Vida de sesión mínima. Mover el reloj no sirve aquí: vencería también el
	// desafío de la ceremonia, y lo que se quiere probar es solo la sesión.
	e := setupWith(t, func(c *passkeys.Config) { c.SessionTTL = 50 * time.Millisecond })
	reference, customer := e.approved(t)
	session := e.enroll(t, reference, newDevice(customer, virtualwebauthn.AuthenticatorOptions{}))
	time.Sleep(100 * time.Millisecond)

	if _, _, err := e.svc.ResolveSession(e.ctx, session.Token); !errors.Is(err, passkeys.ErrSessionInvalid) {
		t.Errorf("sesión vencida = %v, se esperaba ErrSessionInvalid", err)
	}
}
