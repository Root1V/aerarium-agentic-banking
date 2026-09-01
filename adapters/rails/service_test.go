// Tests del adaptador de riel contra el core real.
//
// Cubren los escenarios que en producción rompen sistemas de pago: reenvíos,
// entregas desordenadas, rechazos y —el más caro— la respuesta que nunca llega.
//
// Requiere el core corriendo:
//
//	docker compose -f platform/docker-compose.yml up -d
//	cd core && cargo run --bin aibank-core-server
package rails_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/aibank/aibank/adapters/rails"
	"github.com/aibank/aibank/adapters/rails/sim"
	"github.com/aibank/aibank/clients/go/coreclient"
	corev1 "github.com/aibank/aibank/clients/go/corev1"
	"github.com/google/uuid"
)

type fixture struct {
	svc        *rails.Service
	rail       *sim.Rail
	core       *coreclient.Client
	ctx        context.Context
	customerID string
	alias      string
	accounts   rails.Accounts
}

func coreAddr() string {
	if addr := os.Getenv("CORE_ADDR"); addr != "" {
		return addr
	}
	return "localhost:50051"
}

// setup monta un banco mínimo: producto, cuenta de cliente con saldo, cuentas
// internas de settlement y tránsito, y un riel simulado con el alias destino.
func setup(t *testing.T, funded int64) *fixture {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)

	core, err := coreclient.Dial(ctx, coreAddr())
	if err != nil {
		t.Fatalf("dial: %v", err)
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
			t.Fatalf("crear cuenta interna %s: %v", code, err)
		}
		return acc.Id
	}
	accounts := rails.Accounts{
		SettlementID: internal("rail-settlement", "Posición frente al riel", corev1.AccountType_ACCOUNT_TYPE_ASSET),
		// Pasivo: el dinero en tránsito sigue siendo una obligación del banco,
		// no algo que el banco posea. Ver el comentario de rails.Accounts.
		InTransitID: internal("rail-transit", "Dinero en tránsito", corev1.AccountType_ACCOUNT_TYPE_LIABILITY),
	}
	cash := internal("cash", "Caja", corev1.AccountType_ACCOUNT_TYPE_ASSET)

	customer, err := core.OpenCustomerAccount(ctx, "cust-"+suffix, "Cliente", uuid.NewString(), productCode)
	if err != nil {
		t.Fatalf("abrir cuenta: %v", err)
	}

	if funded > 0 {
		if _, err := core.Post(ctx, "dep-"+suffix, "deposit", []coreclient.Entry{
			coreclient.Debit(cash, funded, "USD"),
			coreclient.Credit(customer.Id, funded, "USD"),
		}, "fondeo inicial"); err != nil {
			t.Fatalf("fondear: %v", err)
		}
	}

	alias := "alias-" + suffix
	rail := sim.New("sim")
	rail.RegisterAlias(alias, "Destinatario", "Otro Banco")

	directory := sim.NewAliasDirectory()
	myAlias := "mio-" + suffix
	directory.Register(myAlias, customer.Id)
	rail.RegisterAlias(myAlias, "Cliente AIBank", "AIBank")

	return &fixture{
		svc:        rails.NewService(core, rail, directory, accounts),
		rail:       rail,
		core:       core,
		ctx:        ctx,
		customerID: customer.Id,
		alias:      myAlias,
		accounts:   accounts,
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

// externalAlias devuelve un alias de destino fuera del banco.
func (f *fixture) externalAlias(t *testing.T) string {
	t.Helper()
	alias := "ext-" + uuid.NewString()
	f.rail.RegisterAlias(alias, "Destinatario externo", "Otro Banco")
	return alias
}

// ---------------------------------------------------------------- entrantes

func TestAcreditacionEntranteAbonaAlCliente(t *testing.T) {
	f := setup(t, 0)

	credit := sim.EmitCredit(f.alias, 120_000000, "USD", "Pagador")
	result, err := f.svc.HandleInboundCredit(f.ctx, credit)
	if err != nil {
		t.Fatalf("acreditar: %v", err)
	}
	if result.Duplicate {
		t.Error("la primera acreditación no es duplicado")
	}
	if got := f.balance(t, f.customerID); got != 120_000000 {
		t.Errorf("saldo del cliente = %d, se esperaba 12000", got)
	}
	if got := f.balance(t, f.accounts.SettlementID); got != 120_000000 {
		t.Errorf("posición frente al riel = %d: entrar dinero aumenta el activo", got)
	}
}

// El caso más frecuente en producción: el riel no recibió el ACK y reenvía.
func TestNotificacionDuplicadaAcreditaUnaSolaVez(t *testing.T) {
	f := setup(t, 0)
	credit := sim.EmitCredit(f.alias, 80_000000, "USD", "Pagador")

	first, err := f.svc.HandleInboundCredit(f.ctx, credit)
	if err != nil {
		t.Fatalf("primera entrega: %v", err)
	}
	second, err := f.svc.HandleInboundCredit(f.ctx, sim.Duplicate(credit))
	if err != nil {
		t.Fatalf("reenvío: %v", err)
	}

	if !second.Duplicate {
		t.Error("el reenvío debe reconocerse como duplicado")
	}
	if second.TransactionID != first.TransactionID {
		t.Error("el reenvío debe resolver a la misma transacción")
	}
	if got := f.balance(t, f.customerID); got != 80_000000 {
		t.Errorf("saldo = %d, se esperaba 8000: el reenvío duplicó el abono", got)
	}
}

func TestEntregasDesordenadasNoAlteranElSaldoFinal(t *testing.T) {
	f := setup(t, 0)

	credits := []rails.InboundCredit{
		sim.EmitCredit(f.alias, 10_000000, "USD", "A"),
		sim.EmitCredit(f.alias, 20_000000, "USD", "B"),
		sim.EmitCredit(f.alias, 30_000000, "USD", "C"),
	}
	for _, c := range sim.Shuffle(credits) {
		if _, err := f.svc.HandleInboundCredit(f.ctx, c); err != nil {
			t.Fatalf("acreditar: %v", err)
		}
	}

	if got := f.balance(t, f.customerID); got != 60_000000 {
		t.Errorf("saldo = %d, se esperaba 6000", got)
	}
}

// Ráfaga de reintentos simultáneos del mismo webhook.
func TestReenviosConcurrentesAcreditanUnaVez(t *testing.T) {
	f := setup(t, 0)
	credit := sim.EmitCredit(f.alias, 45_000000, "USD", "Pagador")

	const attempts = 8
	var wg sync.WaitGroup
	errs := make([]error, attempts)
	start := make(chan struct{})

	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = f.svc.HandleInboundCredit(f.ctx, credit)
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("intento %d: %v", i, err)
		}
	}
	if got := f.balance(t, f.customerID); got != 45_000000 {
		t.Errorf("saldo = %d, se esperaba 4500 pese a %d reenvíos simultáneos", got, attempts)
	}
}

func TestAcreditacionAAliasAjenoNoAsientaNada(t *testing.T) {
	f := setup(t, 0)

	credit := sim.EmitCredit("alias-de-otro-banco", 50_000000, "USD", "Pagador")
	_, err := f.svc.HandleInboundCredit(f.ctx, credit)

	if !errors.Is(err, rails.ErrAliasNotOurs) {
		t.Fatalf("se esperaba ErrAliasNotOurs, se obtuvo %v", err)
	}
	if got := f.balance(t, f.accounts.SettlementID); got != 0 {
		t.Errorf("no debe moverse nada: posición = %d", got)
	}
}

// ---------------------------------------------------------------- salientes

func TestTransferenciaSalienteConfirmadaSaleDelCliente(t *testing.T) {
	f := setup(t, 500_000000)
	target := f.externalAlias(t)

	result, err := f.svc.SendOutbound(f.ctx, rails.OutboundRequest{
		TransferID:    uuid.NewString(),
		FromAccountID: f.customerID,
		ToAlias:       target,
		AmountMicros:   200_000000,
		Currency:      "USD",
	})
	if err != nil {
		t.Fatalf("enviar: %v", err)
	}

	if result.Status != rails.OutboundSettled {
		t.Errorf("estado = %s, se esperaba settled", result.Status)
	}
	if got := f.balance(t, f.customerID); got != 300_000000 {
		t.Errorf("saldo del cliente = %d, se esperaba 30000", got)
	}
	if got := f.balance(t, f.accounts.InTransitID); got != 0 {
		t.Errorf("tránsito = %d, debe quedar saldado tras confirmar", got)
	}
	if got := f.balance(t, f.accounts.SettlementID); got != -200_000000 {
		t.Errorf("posición frente al riel = %d: salir dinero reduce el activo", got)
	}
}

// El core rechaza ANTES de que el riel se entere: no se puede enviar dinero que
// no existe, y el riel no debe recibir una orden que luego habría que revertir.
func TestSinFondosNoSeLlamaAlRiel(t *testing.T) {
	f := setup(t, 50_000000)
	target := f.externalAlias(t)

	_, err := f.svc.SendOutbound(f.ctx, rails.OutboundRequest{
		TransferID:    uuid.NewString(),
		FromAccountID: f.customerID,
		ToAlias:       target,
		AmountMicros:   200_000000,
		Currency:      "USD",
	})

	if !errors.Is(err, coreclient.ErrInsufficientFunds) {
		t.Fatalf("se esperaba ErrInsufficientFunds, se obtuvo %v", err)
	}
	if f.rail.SendCount() != 0 {
		t.Error("el riel no debe recibir la orden si no hay fondos")
	}
	if got := f.balance(t, f.customerID); got != 50_000000 {
		t.Errorf("saldo = %d, no debe alterarse", got)
	}
}

func TestRechazoDelRielDevuelveElDineroAlCliente(t *testing.T) {
	f := setup(t, 300_000000)
	target := f.externalAlias(t)
	f.rail.RejectNext(1)

	result, err := f.svc.SendOutbound(f.ctx, rails.OutboundRequest{
		TransferID:    uuid.NewString(),
		FromAccountID: f.customerID,
		ToAlias:       target,
		AmountMicros:   100_000000,
		Currency:      "USD",
	})
	if err != nil {
		t.Fatalf("enviar: %v", err)
	}

	if result.Status != rails.OutboundReversed {
		t.Errorf("estado = %s, se esperaba reversed", result.Status)
	}
	if got := f.balance(t, f.customerID); got != 300_000000 {
		t.Errorf("saldo = %d: el rechazo debe devolver el dinero íntegro", got)
	}
	if got := f.balance(t, f.accounts.InTransitID); got != 0 {
		t.Errorf("tránsito = %d, debe quedar en cero tras la reversa", got)
	}
}

// El escenario caro: el riel no responde. Revertir sería un error, porque la
// orden pudo haberse procesado del otro lado.
func TestSinRespuestaDelRielElDineroQuedaEnTransitoYNoSeDevuelve(t *testing.T) {
	f := setup(t, 400_000000)
	target := f.externalAlias(t)
	f.rail.FailNext(1)

	result, err := f.svc.SendOutbound(f.ctx, rails.OutboundRequest{
		TransferID:    uuid.NewString(),
		FromAccountID: f.customerID,
		ToAlias:       target,
		AmountMicros:   150_000000,
		Currency:      "USD",
	})
	if err != nil {
		t.Fatalf("enviar: %v", err)
	}

	if result.Status != rails.OutboundInTransit {
		t.Fatalf("estado = %s, se esperaba in_transit", result.Status)
	}
	if got := f.balance(t, f.customerID); got != 250_000000 {
		t.Errorf("saldo = %d: el dinero NO debe devolverse ante un desenlace ambiguo", got)
	}
	if got := f.balance(t, f.accounts.InTransitID); got != 150_000000 {
		t.Errorf("tránsito = %d: el limbo debe quedar visible para la conciliación", got)
	}
}

// Reintentar tras un timeout con el MISMO TransferID no debe cobrar dos veces.
func TestReintentoTrasTimeoutNoCobraDosVeces(t *testing.T) {
	f := setup(t, 400_000000)
	target := f.externalAlias(t)
	transferID := uuid.NewString()
	req := rails.OutboundRequest{
		TransferID:    transferID,
		FromAccountID: f.customerID,
		ToAlias:       target,
		AmountMicros:   100_000000,
		Currency:      "USD",
	}

	f.rail.FailNext(1)
	first, err := f.svc.SendOutbound(f.ctx, req)
	if err != nil {
		t.Fatalf("primer intento: %v", err)
	}
	if first.Status != rails.OutboundInTransit {
		t.Fatalf("primer intento = %s, se esperaba in_transit", first.Status)
	}

	// El riel ya responde: el reintento reusa la reserva y cierra la operación.
	second, err := f.svc.SendOutbound(f.ctx, req)
	if err != nil {
		t.Fatalf("reintento: %v", err)
	}
	if second.Status != rails.OutboundSettled {
		t.Errorf("reintento = %s, se esperaba settled", second.Status)
	}
	if got := f.balance(t, f.customerID); got != 300_000000 {
		t.Errorf("saldo = %d: el reintento cobró dos veces al cliente", got)
	}
	if got := f.balance(t, f.accounts.InTransitID); got != 0 {
		t.Errorf("tránsito = %d, debe quedar saldado", got)
	}
}

func TestAliasDesconocidoNoMueveDinero(t *testing.T) {
	f := setup(t, 100_000000)

	_, err := f.svc.SendOutbound(f.ctx, rails.OutboundRequest{
		TransferID:    uuid.NewString(),
		FromAccountID: f.customerID,
		ToAlias:       "no-existe",
		AmountMicros:   10_000000,
		Currency:      "USD",
	})

	if !errors.Is(err, rails.ErrUnknownAlias) {
		t.Fatalf("se esperaba ErrUnknownAlias, se obtuvo %v", err)
	}
	if got := f.balance(t, f.customerID); got != 100_000000 {
		t.Errorf("saldo = %d, no debe alterarse", got)
	}
}
