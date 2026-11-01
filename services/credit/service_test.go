package credit_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/aibank/aibank/clients/go/coreclient"
	corev1 "github.com/aibank/aibank/clients/go/corev1"
	"github.com/aibank/aibank/services/credit"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
)

// fakeRisk decide lo que se le diga y anota lo que recibió.
type fakeRisk struct {
	mu       sync.Mutex
	outcome  string
	limit    int64
	received []credit.AssessmentRequest
}

func (f *fakeRisk) Assess(_ context.Context, req credit.AssessmentRequest) (*credit.Assessment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.received = append(f.received, req)
	reasons := []string{}
	if f.outcome != credit.OutcomeApproved {
		reasons = []string{"Poco historial en la cuenta"}
	}
	record, _ := json.Marshal(map[string]any{"outcome": f.outcome, "limit_micros": f.limit})
	return &credit.Assessment{Outcome: f.outcome, LimitMicros: f.limit, Currency: "USD", Score: 640,
		ModelVersion: "scorecard-v1", Reasons: reasons, Record: record}, nil
}

type env struct {
	ctx     context.Context
	core    *coreclient.Client
	svc     *credit.Service
	risk    *fakeRisk
	cash    string
	product string
}

func setup(t *testing.T, cfg credit.Config) *env {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	addr := os.Getenv("CORE_ADDR")
	if addr == "" {
		addr = "localhost:50051"
	}
	core, err := coreclient.Dial(ctx, addr)
	if err != nil {
		t.Skipf("core no disponible: %v", err)
	}
	t.Cleanup(func() { _ = core.Close() })
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		url = "postgres://aibank:aibank_dev@localhost:5434/aibank?sslmode=disable"
	}
	db, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	suffix := uuid.NewString()[:8]
	product := "AHORRO-" + suffix
	if _, err := core.CreateProduct(ctx, coreclient.NewProduct{Code: product, Name: "Ahorro", Currency: "USD"}); err != nil {
		if coreclient.Retryable(err) {
			t.Skipf("core no disponible: %v", err)
		}
		t.Fatal(err)
	}
	cash, err := core.CreateInternalAccount(ctx, "cash-"+suffix, "Caja", corev1.AccountType_ACCOUNT_TYPE_ASSET, "USD")
	if err != nil {
		t.Fatal(err)
	}
	risk := &fakeRisk{outcome: credit.OutcomeApproved, limit: 500_000_000}
	svc := credit.NewService(db, core, risk, cfg)
	if err := svc.Migrate(ctx); err != nil {
		t.Skipf("base no disponible: %v", err)
	}
	return &env{ctx: ctx, core: core, svc: svc, risk: risk, cash: cash.Id, product: product}
}

// customer abre una cuenta de ahorro con saldo.
func (e *env) customer(t *testing.T, balance int64) (customerID, accountID string) {
	t.Helper()
	customerID = uuid.NewString()
	account, err := e.core.OpenCustomerAccount(e.ctx, "acc-"+customerID, "Ana Pérez", customerID, e.product)
	if err != nil {
		t.Fatal(err)
	}
	if balance > 0 {
		if _, err := e.core.Post(e.ctx, "dep-"+customerID, "deposit", []coreclient.Entry{
			coreclient.Debit(e.cash, balance, "USD"), coreclient.Credit(account.Id, balance, "USD"),
		}, "depósito"); err != nil {
			t.Fatal(err)
		}
	}
	return customerID, account.Id
}

func TestUnaSolicitudAprobadaAbreLaTarjetaConSuDecision(t *testing.T) {
	e := setup(t, credit.Config{})
	customer, account := e.customer(t, 100_000_000)

	d, err := e.svc.Apply(e.ctx, customer, account, "Ana Pérez")
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != credit.OutcomeApproved || d.Line == nil {
		t.Fatalf("decisión = %+v", d)
	}
	if d.Line.Limit.AmountMicros != 500_000_000 || d.Line.DecisionRef != d.ApplicationID {
		t.Errorf("línea = %+v", d.Line)
	}
	if d.Line.AnnualRateBps != 4500 || d.Line.PaymentDays != 20 {
		t.Errorf("condiciones = %d bps, %d días", d.Line.AnnualRateBps, d.Line.PaymentDays)
	}
	// El motor recibió lo observado en la cuenta: el depósito.
	got := e.risk.received[0]
	if got.CustomerID != customer || len(got.Movements) != 1 || got.Movements[0].Direction != "in" {
		t.Errorf("lo que se evaluó: %+v", got)
	}

	if _, err := e.svc.Apply(e.ctx, customer, account, "Ana Pérez"); !errors.Is(err, credit.ErrAlreadyHasLine) {
		t.Errorf("segunda solicitud con tarjeta: %v", err)
	}
}

func TestSinHistorialNoSeAbreNadaYSeDiceQueFalta(t *testing.T) {
	e := setup(t, credit.Config{})
	e.risk.outcome, e.risk.limit = credit.OutcomeNeedsMoreHistory, 0
	customer, account := e.customer(t, 0)

	d, err := e.svc.Apply(e.ctx, customer, account, "Ana Pérez")
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != credit.OutcomeNeedsMoreHistory || d.Line != nil || len(d.Reasons) == 0 {
		t.Fatalf("decisión = %+v", d)
	}
	if _, err := e.svc.Line(e.ctx, customer); !errors.Is(err, credit.ErrNoLine) {
		t.Errorf("se abrió una línea sin aprobación: %v", err)
	}

	// Pedirla otra vez enseguida devuelve la misma decisión sin volver a evaluar.
	again, err := e.svc.Apply(e.ctx, customer, account, "Ana Pérez")
	if err != nil || again.ApplicationID != d.ApplicationID {
		t.Errorf("segunda solicitud: %+v %v", again, err)
	}
	if len(e.risk.received) != 1 {
		t.Errorf("el motor evaluó %d veces", len(e.risk.received))
	}
}

func TestEnElSandboxSeSumaUnHistorialSinteticoYQuedaDicho(t *testing.T) {
	e := setup(t, credit.Config{SyntheticHistory: true})
	customer, account := e.customer(t, 0)
	d, err := e.svc.Apply(e.ctx, customer, account, "Ana Pérez")
	if err != nil {
		t.Fatal(err)
	}
	if !d.SyntheticHistory {
		t.Error("la decisión no dice que el historial fue sintético")
	}
	synthetic := 0
	for _, m := range e.risk.received[0].Movements {
		if m.Kind == "sandbox_history" {
			synthetic++
		}
	}
	if synthetic == 0 {
		t.Error("el motor no recibió el historial sintético")
	}
}

func TestUsarYPagarLaTarjeta(t *testing.T) {
	e := setup(t, credit.Config{})
	customer, account := e.customer(t, 100_000_000)
	if _, err := e.svc.Apply(e.ctx, customer, account, "Ana Pérez"); err != nil {
		t.Fatal(err)
	}

	if _, err := e.svc.Draw(e.ctx, customer, account, 200_000_000, "d1"); err != nil {
		t.Fatalf("disposición: %v", err)
	}
	line, _ := e.svc.Line(e.ctx, customer)
	if line.Outstanding.AmountMicros != 200_000_000 || line.Available.AmountMicros != 300_000_000 {
		t.Errorf("tras usar 200: debe %d, disponible %d", line.Outstanding.AmountMicros, line.Available.AmountMicros)
	}
	if _, err := e.svc.Draw(e.ctx, customer, account, 300_000_001, "d2"); !errors.Is(err, coreclient.ErrCreditLimitExceeded) {
		t.Errorf("por encima del límite: %v", err)
	}
	// La misma clave no dispone dos veces.
	if r, err := e.svc.Draw(e.ctx, customer, account, 200_000_000, "d1"); err != nil || !r.Replayed {
		t.Errorf("reintento: %+v %v", r, err)
	}

	if _, err := e.svc.Pay(e.ctx, customer, account, 50_000_000, "p1"); err != nil {
		t.Fatalf("pago: %v", err)
	}
	if _, err := e.svc.Pay(e.ctx, customer, account, 150_000_001, "p2"); !errors.Is(err, coreclient.ErrOverpayment) {
		t.Errorf("pagar de más: %v", err)
	}
	line, _ = e.svc.Line(e.ctx, customer)
	if line.Outstanding.AmountMicros != 150_000_000 {
		t.Errorf("tras pagar 50: debe %d", line.Outstanding.AmountMicros)
	}
}

// El cliente HTTP del motor entiende el expediente y conserva el JSON entero.
func TestElClienteHTTPDelMotor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/assessments" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"outcome":"approved","limit_micros":700000000,"currency":"USD","score":720,` +
			`"model_version":"scorecard-v1","reasons":[],"features":{"months_of_history":6}}`))
	}))
	defer srv.Close()

	a, err := credit.NewHTTPAssessor(srv.URL).Assess(context.Background(), credit.AssessmentRequest{CustomerID: "c"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Outcome != "approved" || a.LimitMicros != 700_000_000 || a.Score != 720 {
		t.Errorf("expediente = %+v", a)
	}
	var record map[string]any
	_ = json.Unmarshal(a.Record, &record)
	if record["features"] == nil {
		t.Error("el expediente archivado perdió las características")
	}

	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":"invalid_request"}`))
	}))
	defer down.Close()
	if _, err := credit.NewHTTPAssessor(down.URL).Assess(context.Background(), credit.AssessmentRequest{}); !errors.Is(err, credit.ErrRiskUnavailable) {
		t.Errorf("motor que rechaza: %v", err)
	}
}

// El ciclo desde el servicio: cortar, ver el estado de cuenta, caer en mora y
// salir de ella pagando, que desbloquea en el momento.
func TestElCicloDeLaTarjeta(t *testing.T) {
	e := setup(t, credit.Config{})
	customer, account := e.customer(t, 500_000_000)
	if _, err := e.svc.Apply(e.ctx, customer, account, "Ana Pérez"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.PassDueDate(e.ctx, customer); !errors.Is(err, credit.ErrNoStatement) {
		t.Errorf("sin estado de cuenta: %v", err)
	}
	if _, err := e.svc.Draw(e.ctx, customer, account, 200_000_000, "d1"); err != nil {
		t.Fatal(err)
	}

	statement, err := e.svc.CloseCycleNow(e.ctx, customer)
	if err != nil {
		t.Fatal(err)
	}
	if statement.Closing.AmountMicros != 200_000_000 || statement.MinimumPayment.AmountMicros != 10_000_000 {
		t.Errorf("estado de cuenta = cierre %d, mínimo %d", statement.Closing.AmountMicros, statement.MinimumPayment.AmountMicros)
	}

	line, err := e.svc.PassDueDate(e.ctx, customer)
	if err != nil {
		t.Fatal(err)
	}
	if line.Status != corev1.CreditLineStatus_CREDIT_LINE_STATUS_BLOCKED {
		t.Fatalf("sin pagar el mínimo tras el vencimiento: %v", line.Status)
	}
	if _, err := e.svc.Draw(e.ctx, customer, account, 1_000_000, "d2"); !errors.Is(err, coreclient.ErrCreditLineInactive) {
		t.Errorf("usar bloqueada: %v", err)
	}

	if _, err := e.svc.Pay(e.ctx, customer, account, 10_000_000, "p1"); err != nil {
		t.Fatal(err)
	}
	line, _ = e.svc.Line(e.ctx, customer)
	if line.Status != corev1.CreditLineStatus_CREDIT_LINE_STATUS_ACTIVE {
		t.Errorf("tras pagar el mínimo sigue %v", line.Status)
	}
	statements, _ := e.svc.Statements(e.ctx, customer, 5)
	if len(statements) != 1 || statements[0].PaidSinceCut.AmountMicros != 10_000_000 {
		t.Errorf("estados de cuenta = %v", statements)
	}
}

// Apartar en un bolsillo y devolverlo no llega al motor: si llegara, mover dinero
// de ida y vuelta parecería ingreso, y subiría el límite de la tarjeta.
func TestLosMovimientosDeBolsilloNoLleganAlMotor(t *testing.T) {
	e := setup(t, credit.Config{})
	customer, account := e.customer(t, 100_000_000)
	pocket, err := e.core.OpenPocket(e.ctx, account, "PK-"+uuid.NewString(), "Ahorro")
	if err != nil {
		t.Fatal(err)
	}
	for i, way := range [][2]string{{account, pocket.Id}, {pocket.Id, account}} {
		if _, err := e.core.MovePocket(e.ctx, fmt.Sprintf("pk-%d-%s", i, customer), way[0], way[1],
			60_000_000, "USD", "bolsillo"); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := e.svc.Apply(e.ctx, customer, account, "Ana Pérez"); err != nil {
		t.Fatal(err)
	}
	sent := e.risk.received[len(e.risk.received)-1].Movements
	if len(sent) != 1 || sent[0].Kind != "deposit" {
		t.Errorf("lo que vio el motor: %+v", sent)
	}
}
