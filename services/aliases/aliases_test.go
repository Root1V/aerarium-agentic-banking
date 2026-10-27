package aliases_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	mrand "math/rand"
	"os"
	"testing"

	"github.com/aibank/aibank/services/aliases"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
)

func setup(t *testing.T) (*aliases.Service, *aliases.SandboxSender, context.Context) {
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
	sender := aliases.NewSandboxSender()
	svc := aliases.NewService(db, sender)
	ctx := context.Background()
	if err := svc.Migrate(ctx); err != nil {
		t.Skipf("base no disponible: %v", err)
	}
	return svc, sender, ctx
}

// Un número al azar por prueba: el directorio es compartido entre corridas.
func phone() string { return fmt.Sprintf("9%08d", mrand.Intn(100_000_000)) }

func TestNumerosQueSeAceptan(t *testing.T) {
	for input, want := range map[string]string{
		"987654321": "+51987654321", "987 654 321": "+51987654321",
		"987-654-321": "+51987654321", "+51 987 654 321": "+51987654321",
	} {
		if got, err := aliases.NormalizePhone(input); err != nil || got != want {
			t.Errorf("%q → %q, %v", input, got, err)
		}
	}
	for _, input := range []string{"", "12345678", "887654321", "9876543210", "014567890"} {
		if _, err := aliases.NormalizePhone(input); !errors.Is(err, aliases.ErrInvalidPhone) {
			t.Errorf("%q aceptado", input)
		}
	}
}

func TestRegistrarUnNumeroConElCodigoYEncontrarlo(t *testing.T) {
	svc, sender, ctx := setup(t)
	customer, number := uuid.NewString(), phone()

	v, err := svc.StartVerification(ctx, customer, "acc-1", number)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Resolve(ctx, number); !errors.Is(err, aliases.ErrNotFound) {
		t.Fatalf("antes de verificar ya figura: %v", err)
	}
	if _, err := svc.Verify(ctx, customer, v.ID, sender.LastCode(v.Phone)); err != nil {
		t.Fatal(err)
	}
	found, err := svc.Resolve(ctx, number)
	if err != nil || found.AccountID != "acc-1" {
		t.Errorf("resolver: %+v %v", found, err)
	}
	if _, err := svc.Verify(ctx, customer, v.ID, sender.LastCode(v.Phone)); !errors.Is(err, aliases.ErrVerificationNotFound) {
		t.Errorf("el código sirvió dos veces: %v", err)
	}
}

func TestCincoCodigosMalosAgotanLaVerificacion(t *testing.T) {
	svc, sender, ctx := setup(t)
	customer := uuid.NewString()
	v, _ := svc.StartVerification(ctx, customer, "acc-1", phone())
	wrong := "000000"
	if sender.LastCode(v.Phone) == wrong {
		wrong = "111111"
	}
	for want := 4; want >= 1; want-- {
		_, err := svc.Verify(ctx, customer, v.ID, wrong)
		var e *aliases.WrongCodeError
		if !errors.As(err, &e) || e.Remaining != want {
			t.Fatalf("err = %v, se esperaban %d intentos", err, want)
		}
	}
	if _, err := svc.Verify(ctx, customer, v.ID, wrong); !errors.Is(err, aliases.ErrTooManyAttempts) {
		t.Fatalf("quinto: %v", err)
	}
	if _, err := svc.Verify(ctx, customer, v.ID, sender.LastCode(v.Phone)); !errors.Is(err, aliases.ErrTooManyAttempts) {
		t.Errorf("el código bueno tras agotar los intentos: %v", err)
	}
}

// El código de otro cliente no sirve, aunque se conozca el id.
func TestLaVerificacionEsDeQuienLaPidio(t *testing.T) {
	svc, sender, ctx := setup(t)
	v, _ := svc.StartVerification(ctx, uuid.NewString(), "acc-1", phone())
	if _, err := svc.Verify(ctx, uuid.NewString(), v.ID, sender.LastCode(v.Phone)); !errors.Is(err, aliases.ErrVerificationNotFound) {
		t.Errorf("err = %v", err)
	}
}

// Los números se reasignan: quien lo verifica hoy se lo queda.
func TestUnNumeroVerificadoPasaAQuienLoVerifico(t *testing.T) {
	svc, _, ctx := setup(t)
	number := phone()
	before, after := uuid.NewString(), uuid.NewString()
	if _, err := svc.RegisterVerified(ctx, before, "acc-antes", number); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RegisterVerified(ctx, after, "acc-ahora", number); err != nil {
		t.Fatal(err)
	}
	found, _ := svc.Resolve(ctx, number)
	if found.AccountID != "acc-ahora" {
		t.Errorf("el número quedó en %s", found.AccountID)
	}
	if _, err := svc.Mine(ctx, before); !errors.Is(err, aliases.ErrNotFound) {
		t.Errorf("el dueño anterior lo conserva: %v", err)
	}
}

// Un cliente tiene un solo número: registrar otro reemplaza el anterior.
func TestUnNumeroPorCliente(t *testing.T) {
	svc, _, ctx := setup(t)
	customer, first, second := uuid.NewString(), phone(), phone()
	_, _ = svc.RegisterVerified(ctx, customer, "acc-1", first)
	_, _ = svc.RegisterVerified(ctx, customer, "acc-1", second)
	if _, err := svc.Resolve(ctx, first); !errors.Is(err, aliases.ErrNotFound) {
		t.Errorf("el número anterior sigue: %v", err)
	}
	mine, _ := svc.Mine(ctx, customer)
	if want, _ := aliases.NormalizePhone(second); mine.Phone != want {
		t.Errorf("mío = %s", mine.Phone)
	}
	if err := svc.Remove(ctx, customer); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Resolve(ctx, second); !errors.Is(err, aliases.ErrNotFound) {
		t.Errorf("quitar no lo sacó del directorio: %v", err)
	}
}

func TestElNombreSeMuestraAMedias(t *testing.T) {
	for in, want := range map[string]string{
		"Juan Pérez (prueba)": "Juan P.", "Carmen Huaman Díaz": "Carmen H.", "Ana Pérez Quispe": "Ana P.", "Liesel": "Liesel", "": "",
	} {
		if got := aliases.MaskName(in); got != want {
			t.Errorf("%q → %q, se esperaba %q", in, got, want)
		}
	}
}
