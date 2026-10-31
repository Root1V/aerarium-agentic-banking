package control_test

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aibank/aibank/services/control"
	"github.com/aibank/aibank/services/stepup"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
)

func setup(t *testing.T) (*control.Service, *stepup.Service, *sql.DB, context.Context) {
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
	payees := stepup.NewService(db, stepup.Policy{AmountThreshold: 500_000_000})
	if err := payees.Migrate(ctx); err != nil {
		t.Skipf("base no disponible: %v", err)
	}
	svc := control.NewService(db, payees)
	if err := svc.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return svc, payees, db, ctx
}

func holder() control.Actor {
	return control.Actor{Kind: control.ActorHolder, ID: "device-1"}
}

func TestLaPersonaConfirmaSiempreLoDeAltoImpacto(t *testing.T) {
	svc, _, _, ctx := setup(t)
	customer := uuid.NewString()
	d, err := svc.Decide(ctx, control.Request{CustomerID: customer, Actor: holder(),
		Action: control.ActionGrantPermission, Operation: []byte("grant_permission\nconsent=c1")})
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != control.Confirm || d.Reason != control.ReasonHighImpact || d.Level != control.LevelHuman {
		t.Errorf("conceder un permiso: %+v", d)
	}
}

func TestLasTransferenciasSiguenLaPoliticaDeStepup(t *testing.T) {
	svc, payees, _, ctx := setup(t)
	customer := uuid.NewString()
	transfer := func(to string, amount int64) control.Decision {
		t.Helper()
		d, err := svc.Decide(ctx, control.Request{CustomerID: customer, Actor: holder(),
			Action: control.ActionTransfer, Counterparty: to, AmountMicros: amount})
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	if d := transfer("ana", 1_000_000); d.Outcome != control.Confirm || d.Reason != control.ReasonNewPayee {
		t.Errorf("destinatario nuevo: %+v", d)
	}
	if err := payees.Remember(ctx, customer, "ana"); err != nil {
		t.Fatal(err)
	}
	if d := transfer("ana", 1_000_000); d.Outcome != control.Allow {
		t.Errorf("conocido y poco: %+v", d)
	}
	if d := transfer("ana", 500_000_000); d.Outcome != control.Confirm || d.Reason != control.ReasonAmount {
		t.Errorf("conocido y desde el umbral: %+v", d)
	}
}

func TestQuienNoEsLaPersonaNecesitaUnPermisoQueLoCubra(t *testing.T) {
	svc, _, _, ctx := setup(t)
	customer := uuid.NewString()
	agent := control.Actor{Kind: control.ActorAgent, ID: "mercatus"}
	ten := int64(10_000_000)
	valid := control.Grant{Ref: "mandate:m1", Level: control.LevelMandate,
		Actions: []control.Action{control.ActionMandatePayment}, MaxPerOperation: &ten,
		ExpiresAt: time.Now().Add(time.Hour)}

	cases := []struct {
		name   string
		action control.Action
		amount int64
		grant  func() *control.Grant
		want   control.Reason
	}{
		{"sin permiso", control.ActionMandatePayment, 1, func() *control.Grant { return nil }, control.ReasonNoGrant},
		{"vencido", control.ActionMandatePayment, 1, func() *control.Grant {
			g := valid
			g.ExpiresAt = time.Now().Add(-time.Minute)
			return &g
		}, control.ReasonGrantInactive},
		{"revocado", control.ActionMandatePayment, 1, func() *control.Grant {
			g := valid
			g.Revoked = true
			return &g
		}, control.ReasonGrantInactive},
		{"otra acción", control.ActionTransfer, 1, func() *control.Grant { g := valid; return &g }, control.ReasonOutOfScope},
		{"nivel corto", control.ActionMandatePayment, 1, func() *control.Grant {
			g := valid
			g.Level = control.LevelRecommend
			return &g
		}, control.ReasonLevelTooLow},
		{"sobre el tope", control.ActionMandatePayment, ten + 1, func() *control.Grant { g := valid; return &g }, control.ReasonOverLimit},
		{"sobre lo que queda del total", control.ActionMandatePayment, 2, func() *control.Grant {
			g := valid
			one := int64(1)
			g.Remaining = &one
			return &g
		}, control.ReasonOverLimit},
		// Ni con un permiso que diga cubrirlo: el nivel 4 no se delega.
		{"alto impacto", control.ActionGrantPermission, 0, func() *control.Grant {
			g := valid
			g.Level = control.LevelHuman
			g.Actions = []control.Action{control.ActionGrantPermission}
			return &g
		}, control.ReasonHumanOnly},
		{"fuera del catálogo", control.Action("close_account"), 0, func() *control.Grant { g := valid; return &g }, control.ReasonUnknownAction},
	}
	for _, c := range cases {
		d, err := svc.Decide(ctx, control.Request{CustomerID: customer, Actor: agent,
			Action: c.action, AmountMicros: c.amount, Grant: c.grant()})
		if err != nil {
			t.Fatal(err)
		}
		if d.Outcome != control.Deny || d.Reason != c.want {
			t.Errorf("%s: %+v, quería deny/%s", c.name, d, c.want)
		}
	}

	d, err := svc.Decide(ctx, control.Request{CustomerID: customer, Actor: agent,
		Action: control.ActionMandatePayment, AmountMicros: ten, Grant: &valid})
	if err != nil || d.Outcome != control.Allow {
		t.Errorf("dentro del permiso: %+v, %v", d, err)
	}
}

func TestCadaDecisionYSuConfirmacionQuedanRegistradas(t *testing.T) {
	svc, _, _, ctx := setup(t)
	customer := uuid.NewString()
	req := control.Request{CustomerID: customer, Actor: holder(),
		Action: control.ActionGrantPermission, Operation: []byte("grant_permission\nconsent=c9")}
	if _, err := svc.Decide(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err := svc.Confirmed(ctx, req, true, "device:device-1;method:biometric"); err != nil {
		t.Fatal(err)
	}
	history, err := svc.History(ctx, customer, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 {
		t.Fatalf("registro = %+v", history)
	}
	// Lo último primero: la confirmación, después la decisión que la pidió.
	if history[0].Outcome != "confirmed" || !strings.Contains(history[0].Evidence, "biometric") {
		t.Errorf("confirmación: %+v", history[0])
	}
	if history[1].Outcome != "confirm" || history[1].Reason != control.ReasonHighImpact ||
		history[1].Operation != "grant_permission\nconsent=c9" || history[1].Level != control.LevelHuman {
		t.Errorf("decisión: %+v", history[1])
	}
	if got, _ := svc.History(ctx, uuid.NewString(), 10); len(got) != 0 {
		t.Errorf("el registro de otro cliente: %+v", got)
	}
}

func TestElRegistroNoSePuedeEditarNiBorrar(t *testing.T) {
	svc, _, db, ctx := setup(t)
	customer := uuid.NewString()
	if _, err := svc.Decide(ctx, control.Request{CustomerID: customer, Actor: holder(),
		Action: control.ActionGrantPermission}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE control.decisions SET outcome = 'allow' WHERE customer_id = $1`, customer); err == nil {
		t.Error("se pudo editar una decisión")
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM control.decisions WHERE customer_id = $1`, customer); err == nil {
		t.Error("se pudo borrar una decisión")
	}
}
