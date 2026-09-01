// Tests del adaptador de tarjetas contra el core y PostgreSQL reales.
//
// Cubren lo que distingue una tarjeta de una transferencia: que autorizar y
// cobrar son dos momentos distintos, que el monto puede cambiar entre uno y otro,
// y que una retención que nadie cobra tiene que liberarse.
//
// Requiere:
//
//	docker compose -f platform/docker-compose.yml up -d
//	cd core && cargo run --bin aibank-core-server
package cards_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/aibank/aibank/adapters/cards"
	cardsim "github.com/aibank/aibank/adapters/cards/sim"
	"github.com/aibank/aibank/clients/go/coreclient"
	corev1 "github.com/aibank/aibank/clients/go/corev1"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
)

var (
	migrateOnce sync.Once
	migrateErr  error
)

type fixture struct {
	svc       *cards.Service
	store     *cards.Store
	processor *cardsim.Processor
	core      *coreclient.Client
	ctx       context.Context
	accountID string
	cardID    string
	accounts  cards.Accounts
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

func setup(t *testing.T, funded int64) *fixture {
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
	db.SetMaxOpenConns(4)

	store := cards.NewStore(db)
	migrateOnce.Do(func() { migrateErr = store.Migrate(ctx) })
	if migrateErr != nil {
		t.Fatalf("migrar: %v", migrateErr)
	}

	core, err := coreclient.Dial(ctx, coreAddr())
	if err != nil {
		t.Fatalf("dial core: %v", err)
	}
	t.Cleanup(func() { _ = core.Close() })

	suffix := uuid.NewString()
	productCode := "SIMPLE-" + suffix
	if _, err := core.CreateProduct(ctx, coreclient.NewProduct{
		Code: productCode, Name: "Cuenta simple", Currency: "USD",
	}); err != nil {
		if coreclient.Retryable(err) {
			t.Skipf("core no disponible en %s: %v", coreAddr(), err)
		}
		t.Fatalf("crear producto: %v", err)
	}

	internal := func(code, name string, kind corev1.AccountType) string {
		acc, err := core.CreateInternalAccount(ctx, code+"-"+suffix, name, kind, "USD")
		if err != nil {
			t.Fatalf("crear %s: %v", code, err)
		}
		return acc.Id
	}
	accounts := cards.Accounts{
		// Pasivo: el dinero retenido sigue dentro del banco.
		AuthHoldID: internal("card-hold", "Retenciones de tarjeta", corev1.AccountType_ACCOUNT_TYPE_LIABILITY),
		// Activo: posición frente a la red.
		SettlementID: internal("card-settlement", "Posición frente a la red", corev1.AccountType_ACCOUNT_TYPE_ASSET),
		// Activo: cartera por cobrar de excedentes adelantados.
		OverageID: internal("card-overage", "Excedentes por cobrar", corev1.AccountType_ACCOUNT_TYPE_ASSET),
	}
	cash := internal("cash", "Caja", corev1.AccountType_ACCOUNT_TYPE_ASSET)

	account, err := core.OpenCustomerAccount(ctx, "cust-"+suffix, "Cliente", uuid.NewString(), productCode)
	if err != nil {
		t.Fatalf("abrir cuenta: %v", err)
	}
	if funded > 0 {
		if _, err := core.Post(ctx, "dep-"+suffix, "deposit", []coreclient.Entry{
			coreclient.Debit(cash, funded, "USD"),
			coreclient.Credit(account.Id, funded, "USD"),
		}, "fondeo"); err != nil {
			t.Fatalf("fondear: %v", err)
		}
	}

	processor := cardsim.NewProcessor("sim")
	card, err := processor.IssueCard(ctx, "issue-"+suffix, cards.IssueCardRequest{
		AccountID: account.Id, HolderName: "Cliente", Virtual: true,
	})
	if err != nil {
		t.Fatalf("emitir tarjeta: %v", err)
	}

	return &fixture{
		svc:       cards.NewService(core, store, processor, accounts, "sim"),
		store:     store,
		processor: processor,
		core:      core,
		ctx:       ctx,
		accountID: account.Id,
		cardID:    card.ID,
		accounts:  accounts,
	}
}

func (f *fixture) balance(t *testing.T, accountID string) int64 {
	t.Helper()
	b, err := f.core.GetBalance(f.ctx, accountID)
	if err != nil {
		t.Fatalf("consultar saldo: %v", err)
	}
	if !b.Consistent() {
		t.Errorf("saldo materializado %d != proyección %d", b.AmountMicros, b.ProjectedMicros)
	}
	return b.AmountMicros
}

// ---------------------------------------------------------------- autorizar

// La propiedad que define una autorización: el dinero deja de estar disponible
// para el cliente pero NO sale todavía del banco.
func TestUnaAutorizacionRetieneElDineroSinCobrarlo(t *testing.T) {
	f := setup(t, 500_000000)
	purchase := cardsim.NewPurchase(f.cardID, 120_000000, "USD", "Cafetería")

	decision, err := f.svc.Authorize(f.ctx, purchase.Authorization())
	if err != nil {
		t.Fatalf("autorizar: %v", err)
	}
	if !decision.Approved {
		t.Fatalf("rechazada: %s", decision.Reason)
	}

	if got := f.balance(t, f.accountID); got != 380_000000 {
		t.Errorf("saldo disponible = %d, se esperaba 38000", got)
	}
	if got := f.balance(t, f.accounts.AuthHoldID); got != 120_000000 {
		t.Errorf("retenido = %d, se esperaba 12000", got)
	}
	if got := f.balance(t, f.accounts.SettlementID); got != 0 {
		t.Errorf("posición frente a la red = %d: autorizar no mueve dinero fuera", got)
	}
}

func TestSinFondosSeRechazaConMotivo(t *testing.T) {
	f := setup(t, 50_000000)
	purchase := cardsim.NewPurchase(f.cardID, 200_000000, "USD", "Tienda")

	decision, err := f.svc.Authorize(f.ctx, purchase.Authorization())
	if err != nil {
		t.Fatalf("autorizar: %v", err)
	}

	if decision.Approved {
		t.Fatal("no debía aprobarse")
	}
	if decision.Reason != cards.DeclineInsufficientFunds {
		t.Errorf("motivo = %s, se esperaba insufficient_funds", decision.Reason)
	}
	if got := f.balance(t, f.accountID); got != 50_000000 {
		t.Errorf("saldo = %d: un rechazo no debe mover dinero", got)
	}
	// Un rechazo no puede dejar rastro de retención: el cliente vería inmovilizado
	// un dinero que nunca se movió.
	if got := f.balance(t, f.accounts.AuthHoldID); got != 0 {
		t.Errorf("retenido = %d, debe ser 0", got)
	}
	if _, err := f.store.Get(f.ctx, purchase.NetworkTransactionID); !errors.Is(err, cards.ErrUnknownAuthorization) {
		t.Error("una autorización rechazada no debe quedar registrada como viva")
	}
}

func TestUnaTarjetaCongeladaRechaza(t *testing.T) {
	f := setup(t, 500_000000)
	if err := f.processor.SetStatus(f.ctx, f.cardID, cards.CardFrozen); err != nil {
		t.Fatalf("congelar: %v", err)
	}

	decision, err := f.svc.Authorize(f.ctx, cardsim.NewPurchase(f.cardID, 10_000000, "USD", "Tienda").Authorization())
	if err != nil {
		t.Fatalf("autorizar: %v", err)
	}

	if decision.Approved || decision.Reason != cards.DeclineCardFrozen {
		t.Errorf("se esperaba rechazo por tarjeta congelada, se obtuvo %v/%s", decision.Approved, decision.Reason)
	}
	if got := f.balance(t, f.accountID); got != 500_000000 {
		t.Errorf("saldo = %d, no debe alterarse", got)
	}
}

// La red reenvía autorizaciones cuando no recibe respuesta a tiempo.
func TestUnaAutorizacionRepetidaNoRetieneDosVeces(t *testing.T) {
	f := setup(t, 500_000000)
	request := cardsim.NewPurchase(f.cardID, 90_000000, "USD", "Tienda").Authorization()

	first, err := f.svc.Authorize(f.ctx, request)
	if err != nil {
		t.Fatalf("primera: %v", err)
	}
	second, err := f.svc.Authorize(f.ctx, request)
	if err != nil {
		t.Fatalf("reenvío: %v", err)
	}

	if !second.Approved || !second.Duplicate {
		t.Errorf("el reenvío debe aprobarse como duplicado, se obtuvo %+v", second)
	}
	if second.HoldTransactionID != first.HoldTransactionID {
		t.Error("el reenvío debe apuntar a la misma retención")
	}
	if got := f.balance(t, f.accounts.AuthHoldID); got != 90_000000 {
		t.Errorf("retenido = %d: el reenvío retuvo dos veces", got)
	}
}

func TestAutorizacionesConcurrentesDeLaMismaCompra(t *testing.T) {
	f := setup(t, 500_000000)
	request := cardsim.NewPurchase(f.cardID, 75_000000, "USD", "Tienda").Authorization()

	const attempts = 6
	var wg sync.WaitGroup
	decisions := make([]*cards.AuthorizationDecision, attempts)
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			d, err := f.svc.Authorize(f.ctx, request)
			if err != nil {
				t.Errorf("intento %d: %v", i, err)
				return
			}
			decisions[i] = d
		}(i)
	}
	wg.Wait()

	for i, d := range decisions {
		if d == nil || !d.Approved {
			t.Fatalf("intento %d no aprobado: %+v", i, d)
		}
	}
	if got := f.balance(t, f.accounts.AuthHoldID); got != 75_000000 {
		t.Errorf("retenido = %d pese a %d intentos simultáneos", got, attempts)
	}
}

// ---------------------------------------------------------------- cobrar

func TestElCobroPorElMontoAutorizadoCierraLaRetencion(t *testing.T) {
	f := setup(t, 500_000000)
	purchase := cardsim.NewPurchase(f.cardID, 120_000000, "USD", "Cafetería")

	if _, err := f.svc.Authorize(f.ctx, purchase.Authorization()); err != nil {
		t.Fatalf("autorizar: %v", err)
	}
	result, err := f.svc.Clear(f.ctx, purchase.Clearing(120_000000))
	if err != nil {
		t.Fatalf("cobrar: %v", err)
	}

	if result.OverageMicros != 0 || result.ReturnedMicros != 0 {
		t.Errorf("no debe haber excedente ni devolución: %+v", result)
	}
	if got := f.balance(t, f.accountID); got != 380_000000 {
		t.Errorf("saldo = %d, se esperaba 38000", got)
	}
	if got := f.balance(t, f.accounts.AuthHoldID); got != 0 {
		t.Errorf("retenido = %d: la retención debe quedar cerrada", got)
	}
	if got := f.balance(t, f.accounts.SettlementID); got != -120_000000 {
		t.Errorf("posición frente a la red = %d: ahora sí sale el dinero", got)
	}
}

// El caso de la carga de combustible: autoriza poco, cobra menos de lo retenido.
func TestSiElComercioCobraMenosSeDevuelveLaDiferencia(t *testing.T) {
	f := setup(t, 500_000000)
	purchase := cardsim.NewPurchase(f.cardID, 100_000000, "USD", "Estación de servicio")

	if _, err := f.svc.Authorize(f.ctx, purchase.Authorization()); err != nil {
		t.Fatalf("autorizar: %v", err)
	}
	result, err := f.svc.Clear(f.ctx, purchase.Clearing(60_000000))
	if err != nil {
		t.Fatalf("cobrar: %v", err)
	}

	if result.ReturnedMicros != 40_000000 {
		t.Errorf("devuelto = %d, se esperaba 4000", result.ReturnedMicros)
	}
	if got := f.balance(t, f.accountID); got != 440_000000 {
		t.Errorf("saldo = %d: debe recuperar lo no cobrado", got)
	}
	if got := f.balance(t, f.accounts.AuthHoldID); got != 0 {
		t.Errorf("retenido = %d", got)
	}
}

// El caso de la propina: el restaurante cobra más de lo autorizado.
func TestSiElComercioCobraMasSeCargaLaDiferenciaAlCliente(t *testing.T) {
	f := setup(t, 500_000000)
	purchase := cardsim.NewPurchase(f.cardID, 100_000000, "USD", "Restaurante")

	if _, err := f.svc.Authorize(f.ctx, purchase.Authorization()); err != nil {
		t.Fatalf("autorizar: %v", err)
	}
	result, err := f.svc.Clear(f.ctx, purchase.Clearing(118_000000))
	if err != nil {
		t.Fatalf("cobrar: %v", err)
	}

	if result.OverageMicros != 0 {
		t.Errorf("el cliente tenía saldo: el banco no debía adelantar nada, %+v", result)
	}
	if got := f.balance(t, f.accountID); got != 382_000000 {
		t.Errorf("saldo = %d, se esperaba 38200", got)
	}
	if got := f.balance(t, f.accounts.SettlementID); got != -118_000000 {
		t.Errorf("posición = %d, se esperaba -11800", got)
	}
}

// El caso espinoso: la propina supera el saldo. Rechazar el cobro NO es opción —
// el dinero ya se gastó y la red lo va a exigir igual.
func TestUnExcedenteSinSaldoLoAdelantaElBancoYQuedaRegistrado(t *testing.T) {
	f := setup(t, 100_000000)
	purchase := cardsim.NewPurchase(f.cardID, 100_000000, "USD", "Restaurante")

	if _, err := f.svc.Authorize(f.ctx, purchase.Authorization()); err != nil {
		t.Fatalf("autorizar: %v", err)
	}
	// Tras autorizar, el saldo disponible quedó en cero: no hay con qué cubrir la propina.
	result, err := f.svc.Clear(f.ctx, purchase.Clearing(115_000000))
	if err != nil {
		t.Fatalf("el cobro NO puede fallar: el dinero ya se gastó (%v)", err)
	}

	if result.OverageMicros != 15_000000 {
		t.Errorf("excedente adelantado = %d, se esperaba 1500", result.OverageMicros)
	}
	if got := f.balance(t, f.accountID); got != 0 {
		t.Errorf("saldo del cliente = %d: no debe quedar en negativo", got)
	}
	if got := f.balance(t, f.accounts.OverageID); got != 15_000000 {
		t.Errorf("cartera por cobrar = %d: el adelanto debe quedar visible", got)
	}
	if got := f.balance(t, f.accounts.SettlementID); got != -115_000000 {
		t.Errorf("posición frente a la red = %d", got)
	}

	stored, err := f.store.Get(f.ctx, purchase.NetworkTransactionID)
	if err != nil {
		t.Fatalf("leer autorización: %v", err)
	}
	if stored.OverageMicros != 15_000000 {
		t.Errorf("el adelanto debe quedar registrado, se obtuvo %d", stored.OverageMicros)
	}
}

func TestUnCobroRepetidoNoCobraDosVeces(t *testing.T) {
	f := setup(t, 500_000000)
	purchase := cardsim.NewPurchase(f.cardID, 80_000000, "USD", "Tienda")

	if _, err := f.svc.Authorize(f.ctx, purchase.Authorization()); err != nil {
		t.Fatalf("autorizar: %v", err)
	}
	clearing := purchase.Clearing(80_000000)

	if _, err := f.svc.Clear(f.ctx, clearing); err != nil {
		t.Fatalf("cobrar: %v", err)
	}
	second, err := f.svc.Clear(f.ctx, clearing)
	if err != nil {
		t.Fatalf("reenvío del cobro: %v", err)
	}

	if !second.Duplicate {
		t.Error("el reenvío debe reconocerse como duplicado")
	}
	if got := f.balance(t, f.accountID); got != 420_000000 {
		t.Errorf("saldo = %d: el reenvío cobró dos veces", got)
	}
}

// ---------------------------------------------------------------- reversar

// Sin esto, el dinero de una compra que nadie cobra queda inmovilizado para siempre.
func TestUnaReversaLiberaElDineroRetenido(t *testing.T) {
	f := setup(t, 500_000000)
	purchase := cardsim.NewPurchase(f.cardID, 130_000000, "USD", "Hotel")

	if _, err := f.svc.Authorize(f.ctx, purchase.Authorization()); err != nil {
		t.Fatalf("autorizar: %v", err)
	}
	if err := f.svc.Reverse(f.ctx, purchase.Reversal("el comercio anuló la compra")); err != nil {
		t.Fatalf("reversar: %v", err)
	}

	if got := f.balance(t, f.accountID); got != 500_000000 {
		t.Errorf("saldo = %d: debe recuperarse íntegro", got)
	}
	if got := f.balance(t, f.accounts.AuthHoldID); got != 0 {
		t.Errorf("retenido = %d", got)
	}
	if got := f.balance(t, f.accounts.SettlementID); got != 0 {
		t.Errorf("posición = %d: una reversa no mueve dinero fuera", got)
	}
}

func TestNoSePuedeCobrarUnaCompraYaReversada(t *testing.T) {
	f := setup(t, 500_000000)
	purchase := cardsim.NewPurchase(f.cardID, 50_000000, "USD", "Tienda")

	if _, err := f.svc.Authorize(f.ctx, purchase.Authorization()); err != nil {
		t.Fatalf("autorizar: %v", err)
	}
	if err := f.svc.Reverse(f.ctx, purchase.Reversal("expirada")); err != nil {
		t.Fatalf("reversar: %v", err)
	}

	if _, err := f.svc.Clear(f.ctx, purchase.Clearing(50_000000)); err == nil {
		t.Fatal("cobrar una compra reversada debe fallar")
	}
	if got := f.balance(t, f.accountID); got != 500_000000 {
		t.Errorf("saldo = %d, no debe alterarse", got)
	}
}

func TestUnaReversaRepetidaEsInofensiva(t *testing.T) {
	f := setup(t, 500_000000)
	purchase := cardsim.NewPurchase(f.cardID, 50_000000, "USD", "Tienda")

	if _, err := f.svc.Authorize(f.ctx, purchase.Authorization()); err != nil {
		t.Fatalf("autorizar: %v", err)
	}
	reversal := purchase.Reversal("expirada")
	if err := f.svc.Reverse(f.ctx, reversal); err != nil {
		t.Fatalf("reversar: %v", err)
	}
	if err := f.svc.Reverse(f.ctx, reversal); err != nil {
		t.Fatalf("reenvío de la reversa: %v", err)
	}

	if got := f.balance(t, f.accountID); got != 500_000000 {
		t.Errorf("saldo = %d: la reversa se aplicó dos veces", got)
	}
}

// Las retenciones que nadie cobra son una cola operativa que hay que vigilar.
func TestSeListanLasRetencionesVivasParaLiberarlas(t *testing.T) {
	f := setup(t, 500_000000)
	purchase := cardsim.NewPurchase(f.cardID, 40_000000, "USD", "Hotel")

	if _, err := f.svc.Authorize(f.ctx, purchase.Authorization()); err != nil {
		t.Fatalf("autorizar: %v", err)
	}

	// Con antigüedad cero entran todas las vivas.
	held, err := f.store.HeldOlderThan(f.ctx, 0)
	if err != nil {
		t.Fatalf("listar: %v", err)
	}
	var found bool
	for _, a := range held {
		if a.NetworkTransactionID == purchase.NetworkTransactionID {
			found = true
		}
	}
	if !found {
		t.Error("la retención viva debe aparecer en la cola")
	}

	if err := f.svc.Reverse(f.ctx, purchase.Reversal("vencida")); err != nil {
		t.Fatalf("reversar: %v", err)
	}
	held, _ = f.store.HeldOlderThan(f.ctx, 0)
	for _, a := range held {
		if a.NetworkTransactionID == purchase.NetworkTransactionID {
			t.Error("una retención liberada no debe seguir en la cola")
		}
	}
}
