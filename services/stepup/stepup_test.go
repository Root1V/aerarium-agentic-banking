package stepup_test

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	"github.com/aibank/aibank/services/stepup"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
)

func setup(t *testing.T) (*stepup.Service, context.Context) {
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
	svc := stepup.NewService(db, stepup.Policy{AmountThreshold: 500_000_000})
	ctx := context.Background()
	if err := svc.Migrate(ctx); err != nil {
		t.Skipf("base no disponible: %v", err)
	}
	return svc, ctx
}

func transfer(customer, to string, amount int64) stepup.Transfer {
	return stepup.Transfer{CustomerID: customer, FromAccountID: "a", ToAccountID: to,
		AmountMicros: amount, Currency: "USD", IdempotencyKey: "k"}
}

func TestUnDestinatarioNuevoPideConfirmarAunqueSeaPoco(t *testing.T) {
	svc, ctx := setup(t)
	reason, err := svc.Required(ctx, transfer(uuid.NewString(), "b", 1_000_000))
	if err != nil || reason != stepup.ReasonNewPayee {
		t.Errorf("reason = %q, %v", reason, err)
	}
}

func TestUnDestinatarioConocidoYPocoNoPide(t *testing.T) {
	svc, ctx := setup(t)
	customer := uuid.NewString()
	if err := svc.Remember(ctx, customer, "b"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Remember(ctx, customer, "b"); err != nil {
		t.Fatalf("recordar dos veces: %v", err)
	}
	if reason, _ := svc.Required(ctx, transfer(customer, "b", 499_999_999)); reason != stepup.ReasonNone {
		t.Errorf("conocido y poco: %q", reason)
	}
	if reason, _ := svc.Required(ctx, transfer(customer, "b", 500_000_000)); reason != stepup.ReasonAmount {
		t.Errorf("conocido y desde el umbral: %q", reason)
	}
	// Conocido para un cliente no es conocido para otro.
	if reason, _ := svc.Required(ctx, transfer(uuid.NewString(), "b", 1)); reason != stepup.ReasonNewPayee {
		t.Errorf("otro cliente: %q", reason)
	}
}

func TestLoQueSeFirmaDescribeLaOperacionEntera(t *testing.T) {
	op := string(stepup.Transfer{CustomerID: "c", FromAccountID: "acc-1", ToAccountID: "acc-2",
		AmountMicros: 500_000_000, Currency: "USD", IdempotencyKey: "k-9"}.Operation())
	for _, want := range []string{"from=acc-1", "to=acc-2", "amount=500000000", "currency=USD", "key=k-9"} {
		if !strings.Contains(op, want) {
			t.Errorf("falta %q en %q", want, op)
		}
	}
}

func TestLosRecientesVanDelUltimoAlPrimero(t *testing.T) {
	svc, ctx := setup(t)
	customer := uuid.NewString()
	for _, account := range []string{"ana", "beto", "carla"} {
		if err := svc.Remember(ctx, customer, account); err != nil {
			t.Fatal(err)
		}
	}
	// Volver a enviarle a Ana la sube al principio, sin duplicarla.
	if err := svc.Remember(ctx, customer, "ana"); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Recent(ctx, customer, 10)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "ana,carla,beto" {
		t.Errorf("recientes = %v", got)
	}
	if got, _ := svc.Recent(ctx, customer, 2); len(got) != 2 {
		t.Errorf("con límite 2: %v", got)
	}
	if got, _ := svc.Recent(ctx, uuid.NewString(), 10); len(got) != 0 {
		t.Errorf("los recientes de otro cliente: %v", got)
	}
}
