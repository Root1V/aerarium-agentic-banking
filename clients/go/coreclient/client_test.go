// Test de integración de la frontera Go ↔ Rust.
//
// No usa dobles: levanta el core real y verifica que el contrato se respeta de
// punta a punta, incluida la traducción de los errores de negocio — que es lo que
// un adaptador necesita para decidir si reintenta o informa al cliente.
//
// Requiere el core corriendo:
//
//	docker compose -f platform/docker-compose.yml up -d
//	cd core && cargo run --bin aibank-core-server
//
// Salta los tests si el core no responde (CORE_ADDR para apuntar a otra dirección).
package coreclient_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/aibank/aibank/clients/go/coreclient"
	corev1 "github.com/aibank/aibank/clients/go/corev1"
	"github.com/google/uuid"
)

func coreAddr() string {
	if addr := os.Getenv("CORE_ADDR"); addr != "" {
		return addr
	}
	return "localhost:50051"
}

// setup conecta con el core y crea un producto de cuenta simple para el test.
func setup(t *testing.T, maxBalance, maxTransaction *int64) (*coreclient.Client, context.Context, string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)

	client, err := coreclient.Dial(ctx, coreAddr())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	productCode := "SIMPLE-" + uuid.NewString()
	_, err = client.CreateProduct(ctx, coreclient.NewProduct{
		Code:                productCode,
		Name:                "Cuenta simple",
		Currency:            "USD",
		MaxBalanceMicros:     maxBalance,
		MaxTransactionMicros: maxTransaction,
	})
	if err != nil {
		if coreclient.Retryable(err) {
			t.Skipf("core no disponible en %s: %v", coreAddr(), err)
		}
		t.Fatalf("crear producto: %v", err)
	}
	return client, ctx, productCode
}

// fundedAccounts crea caja interna y una cuenta de cliente con `amount` depositado.
func fundedAccounts(t *testing.T, c *coreclient.Client, ctx context.Context, productCode string, amount int64) (string, string) {
	t.Helper()
	suffix := uuid.NewString()

	cash, err := c.CreateInternalAccount(ctx, "cash-"+suffix, "Caja",
		corev1.AccountType_ACCOUNT_TYPE_ASSET, "USD")
	if err != nil {
		t.Fatalf("crear caja: %v", err)
	}

	customer, err := c.OpenCustomerAccount(ctx, "cust-"+suffix, "Cliente", uuid.NewString(), productCode)
	if err != nil {
		t.Fatalf("abrir cuenta: %v", err)
	}

	if amount > 0 {
		_, err = c.Post(ctx, "dep-"+uuid.NewString(), "deposit", []coreclient.Entry{
			coreclient.Debit(cash.Id, amount, "USD"),
			coreclient.Credit(customer.Id, amount, "USD"),
		}, "depósito inicial")
		if err != nil {
			t.Fatalf("depósito: %v", err)
		}
	}
	return cash.Id, customer.Id
}

func TestDepositoYSaldoCruzanElContrato(t *testing.T) {
	client, ctx, productCode := setup(t, nil, nil)
	_, customer := fundedAccounts(t, client, ctx, productCode, 150_000000)

	balance, err := client.GetBalance(ctx, customer)
	if err != nil {
		t.Fatalf("consultar saldo: %v", err)
	}

	if balance.AmountMicros != 150_000000 {
		t.Errorf("saldo = %d, se esperaba 15000", balance.AmountMicros)
	}
	if balance.Currency != "USD" {
		t.Errorf("moneda = %q, se esperaba USD", balance.Currency)
	}
	if !balance.Consistent() {
		t.Errorf("saldo materializado %d != proyección %d", balance.AmountMicros, balance.ProjectedMicros)
	}
}

func TestFondosInsuficientesLlegaComoErrorTipado(t *testing.T) {
	client, ctx, productCode := setup(t, nil, nil)
	cash, customer := fundedAccounts(t, client, ctx, productCode, 100_000000)

	_, err := client.Post(ctx, "wd-"+uuid.NewString(), "withdrawal", []coreclient.Entry{
		coreclient.Debit(customer, 150_000000, "USD"),
		coreclient.Credit(cash, 150_000000, "USD"),
	}, "retiro mayor al saldo")

	if !errors.Is(err, coreclient.ErrInsufficientFunds) {
		t.Fatalf("se esperaba ErrInsufficientFunds, se obtuvo %v", err)
	}
	if coreclient.Retryable(err) {
		t.Error("fondos insuficientes NO debe marcarse reintentable: reintentar no crea saldo")
	}
}

func TestTopesDelProductoLleganComoErroresDistintos(t *testing.T) {
	balanceCap := int64(500_000000)
	client, ctx, productCode := setup(t, &balanceCap, nil)
	cash, customer := fundedAccounts(t, client, ctx, productCode, 400_000000)

	_, err := client.Post(ctx, "dep-"+uuid.NewString(), "deposit", []coreclient.Entry{
		coreclient.Debit(cash, 200_000000, "USD"),
		coreclient.Credit(customer, 200_000000, "USD"),
	}, "supera el tope de saldo")

	if !errors.Is(err, coreclient.ErrBalanceCapExceeded) {
		t.Fatalf("se esperaba ErrBalanceCapExceeded, se obtuvo %v", err)
	}
	if errors.Is(err, coreclient.ErrInsufficientFunds) {
		t.Error("el tope de saldo no debe confundirse con fondos insuficientes")
	}
}

func TestTopePorOperacion(t *testing.T) {
	txCap := int64(100_000000)
	client, ctx, productCode := setup(t, nil, &txCap)
	cash, customer := fundedAccounts(t, client, ctx, productCode, 0)

	_, err := client.Post(ctx, "dep-"+uuid.NewString(), "deposit", []coreclient.Entry{
		coreclient.Debit(cash, 250_000000, "USD"),
		coreclient.Credit(customer, 250_000000, "USD"),
	}, "supera el tope por operación")

	if !errors.Is(err, coreclient.ErrTransactionCapExceeded) {
		t.Fatalf("se esperaba ErrTransactionCapExceeded, se obtuvo %v", err)
	}
}

func TestSolicitudDesbalanceadaEsRechazada(t *testing.T) {
	client, ctx, productCode := setup(t, nil, nil)
	cash, customer := fundedAccounts(t, client, ctx, productCode, 0)

	_, err := client.Post(ctx, "bad-"+uuid.NewString(), "deposit", []coreclient.Entry{
		coreclient.Debit(cash, 100_000000, "USD"),
		coreclient.Credit(customer, 99_000000, "USD"),
	}, "desbalanceada")

	if !errors.Is(err, coreclient.ErrInvalid) {
		t.Fatalf("se esperaba ErrInvalid, se obtuvo %v", err)
	}
}

// La garantía que más importa a un adaptador de riel: reenviar el mismo webhook
// no duplica el abono.
func TestReenvioConLaMismaClaveNoDuplicaElAbono(t *testing.T) {
	client, ctx, productCode := setup(t, nil, nil)
	cash, customer := fundedAccounts(t, client, ctx, productCode, 0)

	key := "webhook-" + uuid.NewString()
	entries := []coreclient.Entry{
		coreclient.Debit(cash, 75_000000, "USD"),
		coreclient.Credit(customer, 75_000000, "USD"),
	}

	first, err := client.Post(ctx, key, "deposit", entries, "acreditación del riel")
	if err != nil {
		t.Fatalf("primer envío: %v", err)
	}
	if first.Replayed {
		t.Error("el primer envío no puede ser un replay")
	}

	second, err := client.Post(ctx, key, "deposit", entries, "reenvío del riel")
	if err != nil {
		t.Fatalf("reenvío: %v", err)
	}
	if !second.Replayed {
		t.Error("el reenvío debe reportarse como replay")
	}
	if second.TransactionID != first.TransactionID {
		t.Errorf("el reenvío devolvió otra transacción: %s != %s", second.TransactionID, first.TransactionID)
	}

	balance, err := client.GetBalance(ctx, customer)
	if err != nil {
		t.Fatalf("consultar saldo: %v", err)
	}
	if balance.AmountMicros != 75_000000 {
		t.Errorf("saldo = %d, se esperaba 7500: el reenvío duplicó el abono", balance.AmountMicros)
	}
}

func TestMismaClaveConMontoDistintoEsConflicto(t *testing.T) {
	client, ctx, productCode := setup(t, nil, nil)
	cash, customer := fundedAccounts(t, client, ctx, productCode, 0)

	key := "conf-" + uuid.NewString()
	_, err := client.Post(ctx, key, "deposit", []coreclient.Entry{
		coreclient.Debit(cash, 20_000000, "USD"),
		coreclient.Credit(customer, 20_000000, "USD"),
	}, "")
	if err != nil {
		t.Fatalf("primer envío: %v", err)
	}

	_, err = client.Post(ctx, key, "deposit", []coreclient.Entry{
		coreclient.Debit(cash, 99_000000, "USD"),
		coreclient.Credit(customer, 99_000000, "USD"),
	}, "")
	if !errors.Is(err, coreclient.ErrIdempotencyConflict) {
		t.Fatalf("se esperaba ErrIdempotencyConflict, se obtuvo %v", err)
	}
}

// Escenario del riel: N reintentos simultáneos del mismo webhook.
func TestReintentosConcurrentesDelMismoWebhookAcreditanUnaVez(t *testing.T) {
	client, ctx, productCode := setup(t, nil, nil)
	cash, customer := fundedAccounts(t, client, ctx, productCode, 0)

	key := "race-" + uuid.NewString()
	const attempts = 8

	var wg sync.WaitGroup
	results := make([]*coreclient.PostResult, attempts)
	errs := make([]error, attempts)
	start := make(chan struct{})

	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = client.Post(ctx, key, "deposit", []coreclient.Entry{
				coreclient.Debit(cash, 33_000000, "USD"),
				coreclient.Credit(customer, 33_000000, "USD"),
			}, fmt.Sprintf("reintento %d", i))
		}(i)
	}
	close(start)
	wg.Wait()

	replays := 0
	for i, err := range errs {
		if err != nil {
			t.Fatalf("intento %d falló: %v", i, err)
		}
		if results[i].Replayed {
			replays++
		}
		if results[i].TransactionID != results[0].TransactionID {
			t.Errorf("intento %d devolvió otra transacción", i)
		}
	}

	if replays != attempts-1 {
		t.Errorf("replays = %d, se esperaba %d (solo uno debe ser insert real)", replays, attempts-1)
	}

	balance, err := client.GetBalance(ctx, customer)
	if err != nil {
		t.Fatalf("consultar saldo: %v", err)
	}
	if balance.AmountMicros != 33_000000 {
		t.Errorf("saldo = %d, se esperaba 3300 pese a %d reintentos simultáneos", balance.AmountMicros, attempts)
	}
}

func TestCuentaInexistenteEsNotFound(t *testing.T) {
	client, ctx, _ := setup(t, nil, nil)

	_, err := client.GetAccount(ctx, "no-existe-"+uuid.NewString())
	if !errors.Is(err, coreclient.ErrNotFound) {
		t.Fatalf("se esperaba ErrNotFound, se obtuvo %v", err)
	}
}
