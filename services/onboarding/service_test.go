// Tests del alta de clientes contra el core real y PostgreSQL real.
//
// Requiere:
//
//	docker compose -f platform/docker-compose.yml up -d
//	cd core && cargo run --bin aibank-core-server
package onboarding_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/aibank/aibank/adapters/kyc"
	kycsim "github.com/aibank/aibank/adapters/kyc/sim"
	"github.com/aibank/aibank/clients/go/coreclient"
	"github.com/aibank/aibank/services/onboarding"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
)

type fixture struct {
	svc         *onboarding.Service
	core        *coreclient.Client
	verifier    *kycsim.IdentityVerifier
	screener    *kycsim.SanctionsScreener
	ctx         context.Context
	productCode string
}

func databaseURL() string {
	if url := os.Getenv("DATABASE_URL"); url != "" {
		return url
	}
	return "postgres://aibank:aibank_dev@localhost:5434/aibank?sslmode=disable"
}

func coreAddr() string {
	if addr := os.Getenv("CORE_ADDR"); addr != "" {
		return addr
	}
	return "localhost:50051"
}

func setup(t *testing.T) *fixture {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)

	db, err := sql.Open("postgres", databaseURL())
	if err != nil {
		t.Fatalf("abrir base: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(ctx); err != nil {
		t.Skipf("PostgreSQL no disponible: %v", err)
	}

	store := onboarding.NewStore(db)
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrar: %v", err)
	}

	core, err := coreclient.Dial(ctx, coreAddr())
	if err != nil {
		t.Fatalf("dial core: %v", err)
	}
	t.Cleanup(func() { _ = core.Close() })

	productCode := "SIMPLE-" + uuid.NewString()
	if _, err := core.CreateProduct(ctx, coreclient.NewProduct{
		Code: productCode, Name: "Cuenta simple", Currency: "USD",
	}); err != nil {
		if coreclient.Retryable(err) {
			t.Skipf("core no disponible en %s: %v", coreAddr(), err)
		}
		t.Fatalf("crear producto: %v", err)
	}

	verifier := kycsim.NewIdentityVerifier()
	screener := kycsim.NewSanctionsScreener()

	return &fixture{
		svc:         onboarding.NewService(store, core, verifier, screener),
		core:        core,
		verifier:    verifier,
		screener:    screener,
		ctx:         ctx,
		productCode: productCode,
	}
}

func (f *fixture) request(documentNumber string) onboarding.SubmitRequest {
	id := uuid.NewString()
	return onboarding.SubmitRequest{
		ExternalRef: "app-" + id,
		CustomerID:  uuid.NewString(),
		ProductCode: f.productCode,
		AccountCode: "cust-" + id,
		Applicant: kyc.Applicant{
			FullName:       "Persona de Prueba",
			DocumentType:   "DNI",
			DocumentNumber: documentNumber,
			DateOfBirth:    time.Date(1990, 5, 20, 0, 0, 0, 0, time.UTC),
			CountryCode:    "PE",
		},
	}
}

// ---------------------------------------------------------------- feliz

func TestAltaAprobadaAbreLaCuenta(t *testing.T) {
	f := setup(t)

	app, err := f.svc.Submit(f.ctx, f.request("12345678"))
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	if app.Status != onboarding.StatusApproved {
		t.Fatalf("estado = %s, se esperaba approved", app.Status)
	}
	if app.AccountID == "" {
		t.Fatal("una alta aprobada debe dejar la cuenta abierta")
	}

	balance, err := f.core.GetBalance(f.ctx, app.AccountID)
	if err != nil {
		t.Fatalf("consultar saldo: %v", err)
	}
	if balance.AmountMinor != 0 {
		t.Errorf("la cuenta nueva debe nacer en cero, saldo = %d", balance.AmountMinor)
	}
}

// El rastro que el supervisor puede exigir para cualquier aprobación.
func TestElExpedienteConservaLaEvidenciaDeCadaPaso(t *testing.T) {
	f := setup(t)

	app, err := f.svc.Submit(f.ctx, f.request("12345678"))
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	trail, err := f.svc.AuditTrail(f.ctx, app.ID)
	if err != nil {
		t.Fatalf("rastro: %v", err)
	}

	want := []onboarding.Step{
		onboarding.StepIdentityVerification,
		onboarding.StepSanctionsScreening,
		onboarding.StepDecision,
		onboarding.StepAccountOpening,
	}
	if len(trail) != len(want) {
		t.Fatalf("pasos registrados = %d, se esperaban %d", len(trail), len(want))
	}
	for i, step := range want {
		if trail[i].Step != step {
			t.Errorf("paso %d = %s, se esperaba %s", i, trail[i].Step, step)
		}
		if trail[i].Outcome == "" {
			t.Errorf("paso %s sin resultado registrado", step)
		}
	}
	if trail[0].ProviderRef == "" {
		t.Error("la verificación de identidad debe guardar la referencia del proveedor")
	}
}

// ---------------------------------------------------------------- rechazos

func TestPruebaDeVidaFallidaRechazaSinAbrirCuenta(t *testing.T) {
	f := setup(t)
	doc := "99999999"
	f.verifier.SetOutcome(doc, kycsim.Rejected("la selfie no superó la prueba de vida"))

	app, err := f.svc.Submit(f.ctx, f.request(doc))
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	if app.Status != onboarding.StatusRejected {
		t.Fatalf("estado = %s, se esperaba rejected", app.Status)
	}
	if app.AccountID != "" {
		t.Error("un alta rechazada no debe abrir cuenta")
	}
	if app.DecisionReason == "" {
		t.Error("el rechazo debe quedar motivado")
	}
}

// Una coincidencia de listas NO es un rechazo automático: los falsos positivos por
// homonimia son frecuentes y la decisión corresponde a un analista.
func TestCoincidenciaEnListasVaARevisionManual(t *testing.T) {
	f := setup(t)
	doc := "77777777"
	f.screener.AddToWatchlist(doc, "OFAC", "ONU")

	app, err := f.svc.Submit(f.ctx, f.request(doc))
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	if app.Status != onboarding.StatusManualReview {
		t.Fatalf("estado = %s, se esperaba manual_review", app.Status)
	}
	if app.AccountID != "" {
		t.Error("no debe abrirse cuenta antes de resolver la revisión")
	}
}

func TestElAnalistaPuedeAprobarUnaRevisionYSeAbreLaCuenta(t *testing.T) {
	f := setup(t)
	doc := "77777777"
	f.screener.AddToWatchlist(doc, "OFAC")
	req := f.request(doc)

	app, err := f.svc.Submit(f.ctx, req)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if app.Status != onboarding.StatusManualReview {
		t.Fatalf("estado previo = %s", app.Status)
	}

	approved, err := f.svc.ApproveManually(f.ctx, app.ID, "analista:compliance-1", req.AccountCode)
	if err != nil {
		t.Fatalf("aprobar manualmente: %v", err)
	}
	if approved.Status != onboarding.StatusApproved {
		t.Fatalf("estado = %s, se esperaba approved", approved.Status)
	}
	if approved.AccountID == "" {
		t.Error("tras la aprobación manual debe abrirse la cuenta")
	}

	trail, _ := f.svc.AuditTrail(f.ctx, app.ID)
	var found bool
	for _, r := range trail {
		if r.Step == onboarding.StepManualReview && r.Provider == "analista:compliance-1" {
			found = true
		}
	}
	if !found {
		t.Error("la decisión humana debe quedar registrada con su autor")
	}
	// La decisión automática NO se pierde: ambas conviven en el rastro.
	var automatic bool
	for _, r := range trail {
		if r.Step == onboarding.StepDecision && r.Outcome == "manual_review" {
			automatic = true
		}
	}
	if !automatic {
		t.Error("la decisión automática previa debe conservarse junto a la humana")
	}
}

func TestElAnalistaPuedeRechazar(t *testing.T) {
	f := setup(t)
	doc := "77777777"
	f.screener.AddToWatchlist(doc, "OFAC")

	app, err := f.svc.Submit(f.ctx, f.request(doc))
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	rejected, err := f.svc.RejectManually(f.ctx, app.ID, "analista:compliance-1", "coincidencia confirmada")
	if err != nil {
		t.Fatalf("rechazar: %v", err)
	}
	if rejected.Status != onboarding.StatusRejected {
		t.Errorf("estado = %s, se esperaba rejected", rejected.Status)
	}
}

// ---------------------------------------------------------------- reanudación

// La propiedad con impacto directo en costos: reanudar no vuelve a pagar
// verificaciones ya realizadas.
func TestReanudarNoRepiteLosPasosYaPagados(t *testing.T) {
	f := setup(t)
	req := f.request("12345678")

	// El screening cae justo después de verificar la identidad.
	f.screener.FailNext(1)
	_, err := f.svc.Submit(f.ctx, req)
	if err == nil {
		t.Fatal("se esperaba un fallo del proveedor de screening")
	}
	if !errors.Is(err, kyc.ErrProviderUnavailable) {
		t.Fatalf("error = %v, se esperaba ErrProviderUnavailable", err)
	}
	if f.verifier.Calls() != 1 {
		t.Fatalf("verificaciones = %d, se esperaba 1", f.verifier.Calls())
	}

	// Se recupera el expediente y se reanuda.
	pending, err := f.svc.Submit(f.ctx, req)
	if err != nil {
		t.Fatalf("reanudar: %v", err)
	}

	if pending.Status != onboarding.StatusApproved {
		t.Fatalf("estado = %s, se esperaba approved", pending.Status)
	}
	if f.verifier.Calls() != 1 {
		t.Errorf("verificaciones = %d: reanudar volvió a pagar la identidad", f.verifier.Calls())
	}
	if f.screener.Calls() != 1 {
		t.Errorf("screenings = %d, se esperaba 1", f.screener.Calls())
	}
}

// Reenviar la misma solicitud no abre un segundo expediente ni una segunda cuenta.
func TestReenviarLaMismaSolicitudEsIdempotente(t *testing.T) {
	f := setup(t)
	req := f.request("12345678")

	first, err := f.svc.Submit(f.ctx, req)
	if err != nil {
		t.Fatalf("primer envío: %v", err)
	}
	second, err := f.svc.Submit(f.ctx, req)
	if err != nil {
		t.Fatalf("reenvío: %v", err)
	}

	if first.ID != second.ID {
		t.Errorf("el reenvío abrió otro expediente: %s != %s", first.ID, second.ID)
	}
	if first.AccountID != second.AccountID {
		t.Errorf("el reenvío abrió otra cuenta: %s != %s", first.AccountID, second.AccountID)
	}
	if f.verifier.Calls() != 1 {
		t.Errorf("verificaciones = %d: el reenvío volvió a verificar", f.verifier.Calls())
	}
}

func TestUnaCuentaYaAbiertaSeReutilizaAlReintentar(t *testing.T) {
	f := setup(t)
	req := f.request("12345678")

	// La cuenta ya existe con ese código (caída entre abrirla y registrar el paso).
	existing, err := f.core.OpenCustomerAccount(f.ctx, req.AccountCode, "Persona de Prueba", req.CustomerID, f.productCode)
	if err != nil {
		t.Fatalf("abrir cuenta previa: %v", err)
	}

	app, err := f.svc.Submit(f.ctx, req)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if app.AccountID != existing.Id {
		t.Errorf("se abrió una cuenta nueva (%s) en vez de reutilizar %s", app.AccountID, existing.Id)
	}
}
