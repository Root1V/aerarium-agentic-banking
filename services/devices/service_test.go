package devices_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	mrand "math/rand"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/aibank/aibank/adapters/kyc"
	"github.com/aibank/aibank/services/devices"
	"github.com/aibank/aibank/services/onboarding"
	"github.com/aibank/aibank/services/sessions"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
)

// Un teléfono de prueba: dos llaves P-256 de software que firman igual que las
// del chip seguro. El servidor no distingue unas de otras —por eso existirá la
// atestación—, así que lo que se prueba aquí es la política, completa.

type env struct {
	ctx      context.Context
	svc      *devices.Service
	onb      *onboarding.Store
	sessions *sessions.Store
}

func setup(t *testing.T) *env { return setupWith(t, devices.Config{}) }

func setupWith(t *testing.T, cfg devices.Config) *env {
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

	onb := onboarding.NewStore(db)
	if err := onb.Migrate(ctx); err != nil {
		t.Skipf("base no disponible: %v", err)
	}
	sess := sessions.NewStore(db)
	store := devices.NewStore(db)
	for _, m := range []interface{ Migrate(context.Context) error }{sess, store} {
		if err := m.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	return &env{ctx: ctx, svc: devices.NewService(cfg, store, onb, sess), onb: onb, sessions: sess}
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
			DocumentNumber: fmt.Sprintf("%08d", mrand.Intn(100_000_000)),
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

type phone struct {
	bio, possession *ecdsa.PrivateKey
	id              string
}

func newPhone() *phone {
	bio, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	possession, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	return &phone{bio: bio, possession: possession}
}

func spki(k *ecdsa.PrivateKey) []byte {
	der, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
	return der
}

func sign(k *ecdsa.PrivateKey, payload []byte) []byte {
	digest := sha256.Sum256(payload)
	sig, _ := ecdsa.SignASN1(rand.Reader, k, digest[:])
	return sig
}

const goodPIN = "482916"

func (e *env) registration(t *testing.T, p *phone, pin string) devices.Registration {
	t.Helper()
	c, err := e.svc.BeginRegistration(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	return devices.Registration{
		ChallengeID:  c.ID,
		BiometricKey: spki(p.bio), PossessionKey: spki(p.possession),
		BiometricSignature:  sign(p.bio, c.Payload),
		PossessionSignature: sign(p.possession, c.Payload),
		PIN:                 pin, Name: "iPhone de Ana", HardwareBacked: true,
	}
}

// enrolled da de alta a alguien y vincula su teléfono.
func (e *env) enrolled(t *testing.T) (*phone, string) {
	t.Helper()
	reference, customer := e.approved(t)
	p := newPhone()
	res, err := e.svc.EnrollWithReference(e.ctx, reference, e.registration(t, p, goodPIN))
	if err != nil {
		t.Fatalf("vincular: %v", err)
	}
	p.id = res.DeviceID
	return p, customer
}

func (e *env) faceID(t *testing.T, p *phone) (*sessions.Session, error) {
	t.Helper()
	c, err := e.svc.BeginLogin(e.ctx, p.id)
	if err != nil {
		return nil, err
	}
	return e.svc.LoginWithBiometrics(e.ctx, c.ID, sign(p.bio, c.Payload))
}

func (e *env) pin(t *testing.T, p *phone, pin string) (*sessions.Session, error) {
	t.Helper()
	c, err := e.svc.BeginLogin(e.ctx, p.id)
	if err != nil {
		return nil, err
	}
	return e.svc.LoginWithPIN(e.ctx, c.ID, sign(p.possession, c.Payload), pin)
}

func (e *env) factorOf(t *testing.T, s *sessions.Session) sessions.Factor {
	t.Helper()
	p, err := e.sessions.Resolve(e.ctx, s.Token)
	if err != nil {
		t.Fatalf("la sesión no resuelve: %v", err)
	}
	return p.Factor
}

func TestVincularYEntrarConLaCara(t *testing.T) {
	e := setup(t)
	reference, customer := e.approved(t)
	p := newPhone()

	res, err := e.svc.EnrollWithReference(e.ctx, reference, e.registration(t, p, goodPIN))
	if err != nil {
		t.Fatal(err)
	}
	if res.Session.CustomerID != customer {
		t.Fatalf("la sesión es de %s, se esperaba %s", res.Session.CustomerID, customer)
	}
	p.id = res.DeviceID

	session, err := e.faceID(t, p)
	if err != nil {
		t.Fatalf("entrar con la cara: %v", err)
	}
	principal, _ := e.sessions.Resolve(e.ctx, session.Token)
	if principal.CustomerID != customer || principal.Factor != sessions.FactorDeviceBiometric || principal.DeviceRef != p.id {
		t.Errorf("sesión = %+v", principal)
	}
}

func TestEntrarConLaClave(t *testing.T) {
	e := setup(t)
	p, _ := e.enrolled(t)
	session, err := e.pin(t, p, goodPIN)
	if err != nil {
		t.Fatal(err)
	}
	if f := e.factorOf(t, session); f != sessions.FactorDevicePIN {
		t.Errorf("factor = %s; una sesión de clave no puede pasar por una de biometría", f)
	}
}

// La llave de posesión firma sin biometría: si sirviera para el login
// biométrico, un teléfono robado y desbloqueado entraría sin la cara.
func TestLaLlaveSinBiometriaNoSirveComoBiometrica(t *testing.T) {
	e := setup(t)
	p, _ := e.enrolled(t)
	c, _ := e.svc.BeginLogin(e.ctx, p.id)
	if _, err := e.svc.LoginWithBiometrics(e.ctx, c.ID, sign(p.possession, c.Payload)); !errors.Is(err, devices.ErrRejected) {
		t.Errorf("err = %v, se esperaba rechazo", err)
	}
}

// Sin la firma del teléfono, la clave ni se compara: nadie puede probar claves
// desde fuera del teléfono, y esos intentos no gastan los del titular.
func TestLaClaveSinElTelefonoNoSePrueba(t *testing.T) {
	e := setup(t)
	p, _ := e.enrolled(t)
	stranger := newPhone()
	for i := 0; i < 10; i++ {
		c, _ := e.svc.BeginLogin(e.ctx, p.id)
		if _, err := e.svc.LoginWithPIN(e.ctx, c.ID, sign(stranger.possession, c.Payload), goodPIN); !errors.Is(err, devices.ErrRejected) {
			t.Fatalf("intento %d desde otro teléfono: %v", i, err)
		}
	}
	if _, err := e.pin(t, p, goodPIN); err != nil {
		t.Errorf("el titular quedó afectado por intentos ajenos: %v", err)
	}
}

func TestCincoClavesMalasBloqueanElTelefono(t *testing.T) {
	e := setup(t)
	p, _ := e.enrolled(t)

	for want := 4; want >= 1; want-- {
		_, err := e.pin(t, p, "135790")
		var wrong *devices.WrongPINError
		if !errors.As(err, &wrong) || wrong.Remaining != want {
			t.Fatalf("err = %v, se esperaba clave incorrecta con %d intentos", err, want)
		}
	}
	if _, err := e.pin(t, p, "135790"); !errors.Is(err, devices.ErrLocked) {
		t.Fatalf("quinto intento: %v, se esperaba bloqueo", err)
	}
	// Bloqueado es bloqueado: ni la clave buena ni la cara lo abren.
	if _, err := e.pin(t, p, goodPIN); !errors.Is(err, devices.ErrLocked) {
		t.Errorf("clave buena tras el bloqueo: %v", err)
	}
	if _, err := e.faceID(t, p); !errors.Is(err, devices.ErrLocked) {
		t.Errorf("cara tras el bloqueo: %v", err)
	}
}

func TestUnaClaveBuenaReiniciaLosIntentos(t *testing.T) {
	e := setup(t)
	p, _ := e.enrolled(t)
	for i := 0; i < 4; i++ {
		_, _ = e.pin(t, p, "135790")
	}
	if _, err := e.pin(t, p, goodPIN); err != nil {
		t.Fatal(err)
	}
	_, err := e.pin(t, p, "135790")
	var wrong *devices.WrongPINError
	if !errors.As(err, &wrong) || wrong.Remaining != 4 {
		t.Errorf("err = %v, se esperaban 4 intentos tras entrar bien", err)
	}
}

// Los intentos simultáneos no pueden colarse todos por debajo del tope.
func TestIntentosSimultaneosRespetanElTope(t *testing.T) {
	e := setup(t)
	p, _ := e.enrolled(t)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = e.pin(t, p, "135790") }()
	}
	wg.Wait()
	if _, err := e.pin(t, p, goodPIN); !errors.Is(err, devices.ErrLocked) {
		t.Errorf("tras 12 intentos simultáneos: %v, se esperaba bloqueo", err)
	}
}

func TestUnDesafioSirveUnaVez(t *testing.T) {
	e := setup(t)
	p, _ := e.enrolled(t)
	c, _ := e.svc.BeginLogin(e.ctx, p.id)
	sig := sign(p.bio, c.Payload)
	if _, err := e.svc.LoginWithBiometrics(e.ctx, c.ID, sig); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.LoginWithBiometrics(e.ctx, c.ID, sig); !errors.Is(err, devices.ErrChallengeNotFound) {
		t.Errorf("repetición: %v", err)
	}
}

// Un desafío de vinculación no sirve para entrar.
func TestUnDesafioDeVinculacionNoSirveParaEntrar(t *testing.T) {
	e := setup(t)
	p, _ := e.enrolled(t)
	c, _ := e.svc.BeginRegistration(e.ctx)
	if _, err := e.svc.LoginWithBiometrics(e.ctx, c.ID, sign(p.bio, c.Payload)); !errors.Is(err, devices.ErrChallengeNotFound) {
		t.Errorf("err = %v", err)
	}
}

func TestLaReferenciaSoloVinculaElPrimerTelefono(t *testing.T) {
	e := setup(t)
	reference, _ := e.approved(t)
	if _, err := e.svc.EnrollWithReference(e.ctx, reference, e.registration(t, newPhone(), goodPIN)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.EnrollWithReference(e.ctx, reference, e.registration(t, newPhone(), goodPIN)); !errors.Is(err, devices.ErrNotEnrollable) {
		t.Errorf("segundo teléfono con la misma referencia: %v", err)
	}
}

func TestDosVinculacionesSimultaneasDejanUnTelefono(t *testing.T) {
	e := setup(t)
	reference, _ := e.approved(t)
	regs := []devices.Registration{e.registration(t, newPhone(), goodPIN), e.registration(t, newPhone(), goodPIN)}
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range regs {
		wg.Add(1)
		go func() { defer wg.Done(); _, errs[i] = e.svc.EnrollWithReference(e.ctx, reference, regs[i]) }()
	}
	wg.Wait()
	if (errs[0] == nil) == (errs[1] == nil) {
		t.Errorf("errores = %v; se esperaba exactamente un teléfono vinculado", errs)
	}
}

func TestUnAltaNoAprobadaNoVincula(t *testing.T) {
	e := setup(t)
	for _, status := range []onboarding.Status{onboarding.StatusManualReview, onboarding.StatusRejected, onboarding.StatusInProgress} {
		reference, _ := e.application(t, status)
		if _, err := e.svc.EnrollWithReference(e.ctx, reference, e.registration(t, newPhone(), goodPIN)); !errors.Is(err, devices.ErrNotEnrollable) {
			t.Errorf("%s: %v", status, err)
		}
	}
	if _, err := e.svc.EnrollWithReference(e.ctx, uuid.NewString(), e.registration(t, newPhone(), goodPIN)); !errors.Is(err, devices.ErrNotEnrollable) {
		t.Errorf("referencia inventada: %v", err)
	}
}

// Vincular exige probar que el teléfono tiene las dos llaves privadas, no solo
// mandar unas públicas cualquiera.
func TestVincularExigeFirmarConLasDosLlaves(t *testing.T) {
	e := setup(t)
	reference, _ := e.approved(t)
	p, other := newPhone(), newPhone()
	r := e.registration(t, p, goodPIN)
	r.PossessionSignature = sign(other.possession, []byte("otra cosa"))
	if _, err := e.svc.EnrollWithReference(e.ctx, reference, r); !errors.Is(err, devices.ErrRejected) {
		t.Errorf("err = %v", err)
	}
}

func TestLasClavesQueSeAdivinanNoSirven(t *testing.T) {
	e := setup(t)
	reference, _ := e.approved(t)
	for _, pin := range []string{"000000", "111111", "123456", "654321", "345678", "12345", "1234567", "12a456", ""} {
		if _, err := e.svc.EnrollWithReference(e.ctx, reference, e.registration(t, newPhone(), pin)); !errors.Is(err, devices.ErrWeakPIN) {
			t.Errorf("clave %q: %v", pin, err)
		}
	}
	// Las rechazadas no gastaron la referencia.
	if _, err := e.svc.EnrollWithReference(e.ctx, reference, e.registration(t, newPhone(), goodPIN)); err != nil {
		t.Errorf("tras claves débiles, la buena: %v", err)
	}
}

func TestOtroTelefonoConSesion(t *testing.T) {
	e := setup(t)
	_, customer := e.enrolled(t)
	second := newPhone()
	res, err := e.svc.AddDevice(e.ctx, customer, e.registration(t, second, "907251"))
	if err != nil {
		t.Fatal(err)
	}
	second.id = res.DeviceID
	session, err := e.faceID(t, second)
	if err != nil || session.CustomerID != customer {
		t.Errorf("entrar con el segundo teléfono: %v", err)
	}
}

func TestDesvincularCierraTodo(t *testing.T) {
	e := setup(t)
	p, customer := e.enrolled(t)
	open, _ := e.faceID(t, p)

	if err := e.svc.Revoke(e.ctx, uuid.NewString(), p.id); !errors.Is(err, devices.ErrRejected) {
		t.Errorf("otro cliente desvinculó el teléfono: %v", err)
	}
	if err := e.svc.Revoke(e.ctx, customer, p.id); err != nil {
		t.Fatal(err)
	}
	if _, err := e.sessions.Resolve(e.ctx, open.Token); !errors.Is(err, sessions.ErrInvalid) {
		t.Errorf("la sesión abierta en el teléfono sigue viva: %v", err)
	}
	if _, err := e.faceID(t, p); !errors.Is(err, devices.ErrRejected) {
		t.Errorf("entrar con un teléfono desvinculado: %v", err)
	}
}

func TestUnTelefonoDesconocidoSeRechaza(t *testing.T) {
	e := setup(t)
	for _, id := range []string{uuid.NewString(), "no-es-un-uuid", ""} {
		if _, err := e.svc.BeginLogin(e.ctx, id); !errors.Is(err, devices.ErrRejected) {
			t.Errorf("%q: %v", id, err)
		}
	}
}

// -------------------------------------------------------------- confirmar

var transferOp = []byte("transfer\nfrom=a\nto=b\namount=500000000\ncurrency=USD\nkey=k1")

func (e *env) confirmWith(t *testing.T, p *phone, customer string, signed, executed []byte, method devices.Method, pin string) error {
	t.Helper()
	c, err := e.svc.BeginConfirmation(e.ctx, customer, p.id, signed)
	if err != nil {
		t.Fatalf("desafío de confirmación: %v", err)
	}
	key := p.bio
	if method == devices.MethodPIN {
		key = p.possession
	}
	return e.svc.Confirm(e.ctx, customer, p.id, c.ID, executed, method, sign(key, c.Payload), pin)
}

func TestConfirmarUnaOperacionConLaCara(t *testing.T) {
	e := setup(t)
	p, customer := e.enrolled(t)
	if err := e.confirmWith(t, p, customer, transferOp, transferOp, devices.MethodBiometric, ""); err != nil {
		t.Fatal(err)
	}
}

// El vínculo dinámico: lo firmado tiene que ser exactamente lo que se ejecuta.
func TestLaFirmaDeUnaOperacionNoSirveParaOtra(t *testing.T) {
	e := setup(t)
	p, customer := e.enrolled(t)
	other := []byte("transfer\nfrom=a\nto=b\namount=5000000000\ncurrency=USD\nkey=k1")
	if err := e.confirmWith(t, p, customer, transferOp, other, devices.MethodBiometric, ""); !errors.Is(err, devices.ErrRejected) {
		t.Errorf("monto cambiado: %v, se esperaba rechazo", err)
	}
	thief := []byte("transfer\nfrom=a\nto=ladron\namount=500000000\ncurrency=USD\nkey=k1")
	if err := e.confirmWith(t, p, customer, transferOp, thief, devices.MethodBiometric, ""); !errors.Is(err, devices.ErrRejected) {
		t.Errorf("destino cambiado: %v, se esperaba rechazo", err)
	}
}

func TestUnaConfirmacionSirveUnaVez(t *testing.T) {
	e := setup(t)
	p, customer := e.enrolled(t)
	c, _ := e.svc.BeginConfirmation(e.ctx, customer, p.id, transferOp)
	sig := sign(p.bio, c.Payload)
	if err := e.svc.Confirm(e.ctx, customer, p.id, c.ID, transferOp, devices.MethodBiometric, sig, ""); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Confirm(e.ctx, customer, p.id, c.ID, transferOp, devices.MethodBiometric, sig, ""); !errors.Is(err, devices.ErrChallengeNotFound) {
		t.Errorf("repetición: %v", err)
	}
}

// Un desafío emitido para un teléfono no lo puede firmar otro, ni siquiera otro
// del mismo cliente; y nadie pide confirmaciones con el teléfono de otro cliente.
func TestLaConfirmacionEsDeUnTelefono(t *testing.T) {
	e := setup(t)
	p, customer := e.enrolled(t)
	second := newPhone()
	res, err := e.svc.AddDevice(e.ctx, customer, e.registration(t, second, "907251"))
	if err != nil {
		t.Fatal(err)
	}
	second.id = res.DeviceID

	c, _ := e.svc.BeginConfirmation(e.ctx, customer, p.id, transferOp)
	if err := e.svc.Confirm(e.ctx, customer, second.id, c.ID, transferOp, devices.MethodBiometric, sign(second.bio, c.Payload), ""); !errors.Is(err, devices.ErrRejected) {
		t.Errorf("firmado por otro teléfono: %v", err)
	}
	if _, err := e.svc.BeginConfirmation(e.ctx, uuid.NewString(), p.id, transferOp); !errors.Is(err, devices.ErrRejected) {
		t.Errorf("confirmación con el teléfono de otro cliente: %v", err)
	}
}

func TestConfirmarConLaClaveCuentaLosIntentos(t *testing.T) {
	e := setup(t)
	p, customer := e.enrolled(t)
	if err := e.confirmWith(t, p, customer, transferOp, transferOp, devices.MethodPIN, goodPIN); err != nil {
		t.Fatalf("clave buena: %v", err)
	}
	err := e.confirmWith(t, p, customer, transferOp, transferOp, devices.MethodPIN, "135790")
	var wrong *devices.WrongPINError
	if !errors.As(err, &wrong) || wrong.Remaining != 4 {
		t.Errorf("clave mala: %v", err)
	}
	// La llave de posesión sola, sin clave, no confirma como si fuera la cara.
	c, _ := e.svc.BeginConfirmation(e.ctx, customer, p.id, transferOp)
	if err := e.svc.Confirm(e.ctx, customer, p.id, c.ID, transferOp, devices.MethodBiometric, sign(p.possession, c.Payload), ""); !errors.Is(err, devices.ErrRejected) {
		t.Errorf("posesión haciéndose pasar por biometría: %v", err)
	}
}
