// Tests del BFF contra el core real.
//
// El grupo más importante es el de autorización: en una API bancaria, el fallo más
// caro no es un cálculo mal hecho sino responderle a alguien con el dinero de otro.
//
// Requiere:
//
//	docker compose -f platform/docker-compose.yml up -d
//	cd core && cargo run --bin aibank-core-server
package bff_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aibank/aibank/clients/go/coreclient"
	corev1 "github.com/aibank/aibank/clients/go/corev1"
	"github.com/aibank/aibank/services/bff"
	bffsim "github.com/aibank/aibank/services/bff/sim"
	"github.com/google/uuid"
)

type fixture struct {
	srv     *httptest.Server
	core    *coreclient.Client
	auth    *bffsim.Authenticator
	ctx     context.Context
	cash    string
	alice   customer
	bob     customer
	mallory customer
}

type customer struct {
	customerID string
	accountID  string
	token      string
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

	cash, err := core.CreateInternalAccount(ctx, "cash-"+suffix, "Caja",
		corev1.AccountType_ACCOUNT_TYPE_ASSET, "USD")
	if err != nil {
		t.Fatalf("crear caja: %v", err)
	}

	auth := bffsim.NewAuthenticator()
	newCustomer := func(name string, funded int64) customer {
		id := uuid.NewString()
		account, err := core.OpenCustomerAccount(ctx, name+"-"+suffix, name, id, productCode)
		if err != nil {
			t.Fatalf("abrir cuenta de %s: %v", name, err)
		}
		if funded > 0 {
			if _, err := core.Post(ctx, "dep-"+name+"-"+suffix, "deposit", []coreclient.Entry{
				coreclient.Debit(cash.Id, funded, "USD"),
				coreclient.Credit(account.Id, funded, "USD"),
			}, "fondeo"); err != nil {
				t.Fatalf("fondear a %s: %v", name, err)
			}
		}
		return customer{customerID: id, accountID: account.Id, token: auth.Issue(id, "device-"+id)}
	}

	server := bff.NewServer(core, auth, nil)
	ts := httptest.NewServer(server.Handler())
	// Sin esto, Close() espera a que expiren las conexiones keep-alive del cliente
	// de pruebas y cada test paga ~9 s de cierre.
	ts.Client().Transport.(*http.Transport).DisableKeepAlives = true
	t.Cleanup(ts.Close)
	t.Cleanup(ts.CloseClientConnections)

	return &fixture{
		srv: ts, core: core, auth: auth, ctx: ctx, cash: cash.Id,
		alice:   newCustomer("alice", 500_000000),
		bob:     newCustomer("bob", 100_000000),
		mallory: newCustomer("mallory", 0),
	}
}

func (f *fixture) do(t *testing.T, method, path, token string, body any, headers map[string]string) (*http.Response, []byte) {
	t.Helper()

	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("serializar cuerpo: %v", err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}

	req, err := http.NewRequestWithContext(f.ctx, method, f.srv.URL+path, reader)
	if err != nil {
		t.Fatalf("crear petición: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := f.srv.Client().Do(req)
	if err != nil {
		t.Fatalf("ejecutar petición: %v", err)
	}
	defer resp.Body.Close()

	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatalf("leer respuesta: %v", err)
	}
	return resp, buf.Bytes()
}

// ---------------------------------------------------------------- autorización

func TestSinTokenSeRechaza(t *testing.T) {
	f := setup(t)

	resp, _ := f.do(t, http.MethodGet, "/v1/home?account_id="+f.alice.accountID, "", nil, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, se esperaba 401", resp.StatusCode)
	}
}

func TestTokenInvalidoSeRechaza(t *testing.T) {
	f := setup(t)

	resp, _ := f.do(t, http.MethodGet, "/v1/home?account_id="+f.alice.accountID, "token-inventado", nil, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, se esperaba 401", resp.StatusCode)
	}
}

// El fallo clásico de una API bancaria: cambiar un id en la URL y ver el dinero
// de otra persona.
func TestNoSePuedeVerElSaldoDeOtroCliente(t *testing.T) {
	f := setup(t)

	resp, body := f.do(t, http.MethodGet,
		"/v1/accounts/"+f.alice.accountID+"/balance", f.mallory.token, nil, nil)

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, se esperaba 404", resp.StatusCode)
	}
	// La respuesta no debe revelar que la cuenta existe ni de quién es: distinguir
	// "no existe" de "no es tuya" permitiría enumerar cuentas ajenas.
	if strings.Contains(string(body), f.alice.accountID) {
		t.Error("la respuesta filtra el identificador de la cuenta ajena")
	}
}

func TestNoSePuedenVerLosMovimientosDeOtroCliente(t *testing.T) {
	f := setup(t)

	resp, _ := f.do(t, http.MethodGet,
		"/v1/accounts/"+f.alice.accountID+"/movements", f.mallory.token, nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, se esperaba 404", resp.StatusCode)
	}
}

// El intento de fraude directo: enviar dinero desde la cuenta de otro.
func TestNoSePuedeTransferirDesdeLaCuentaDeOtroCliente(t *testing.T) {
	f := setup(t)

	resp, _ := f.do(t, http.MethodPost, "/v1/transfers", f.mallory.token, map[string]any{
		"from_account_id": f.alice.accountID,
		"to_account_id":   f.mallory.accountID,
		"amount_micros":    100_000000,
		"currency":        "USD",
	}, map[string]string{"Idempotency-Key": uuid.NewString()})

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, se esperaba 404", resp.StatusCode)
	}

	balance, err := f.core.GetBalance(f.ctx, f.alice.accountID)
	if err != nil {
		t.Fatalf("consultar saldo: %v", err)
	}
	if balance.AmountMicros != 500_000000 {
		t.Errorf("saldo de la víctima = %d: el intento movió dinero", balance.AmountMicros)
	}
}

// Las cuentas internas del banco no se exponen por el canal del cliente.
func TestUnaCuentaInternaNoEsAccesibleDesdeElCanal(t *testing.T) {
	f := setup(t)

	resp, _ := f.do(t, http.MethodGet, "/v1/accounts/"+f.cash+"/balance", f.alice.token, nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, se esperaba 404", resp.StatusCode)
	}
}

func TestNoSePuedeTransferirHaciaUnaCuentaInterna(t *testing.T) {
	f := setup(t)

	resp, _ := f.do(t, http.MethodPost, "/v1/transfers", f.alice.token, map[string]any{
		"from_account_id": f.alice.accountID,
		"to_account_id":   f.cash,
		"amount_micros":    10_000000,
		"currency":        "USD",
	}, map[string]string{"Idempotency-Key": uuid.NewString()})

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, se esperaba 404", resp.StatusCode)
	}
}

// ---------------------------------------------------------------- lectura

func TestHomeDevuelveCuentaSaldoYMovimientos(t *testing.T) {
	f := setup(t)

	resp, body := f.do(t, http.MethodGet, "/v1/home?account_id="+f.alice.accountID, f.alice.token, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", resp.StatusCode, body)
	}

	var home struct {
		Account struct {
			ID       string `json:"id"`
			Currency string `json:"currency"`
		} `json:"account"`
		Balance struct {
			AmountMicros int64  `json:"amount_micros"`
			Currency    string `json:"currency"`
		} `json:"balance"`
		Movements []struct {
			Sign   int `json:"sign"`
			Amount struct {
				AmountMicros int64 `json:"amount_micros"`
			} `json:"amount"`
		} `json:"movements"`
	}
	if err := json.Unmarshal(body, &home); err != nil {
		t.Fatalf("deserializar: %v", err)
	}

	if home.Account.ID != f.alice.accountID {
		t.Errorf("cuenta = %s", home.Account.ID)
	}
	if home.Balance.AmountMicros != 500_000000 {
		t.Errorf("saldo = %d, se esperaba 50000", home.Balance.AmountMicros)
	}
	if len(home.Movements) != 1 {
		t.Fatalf("movimientos = %d, se esperaba 1", len(home.Movements))
	}
	if home.Movements[0].Sign != 1 {
		t.Errorf("un depósito debe sumar (sign=1), se obtuvo %d", home.Movements[0].Sign)
	}
}

// El dinero nunca puede viajar como decimal: JSON no distingue enteros de
// flotantes y el consumidor acabaría haciendo aritmética binaria con saldos.
func TestElDineroViajaComoEnteroEnMicras(t *testing.T) {
	f := setup(t)

	_, body := f.do(t, http.MethodGet, "/v1/accounts/"+f.alice.accountID+"/balance", f.alice.token, nil, nil)

	raw := string(body)
	// El valor completo, no un prefijo: con importes en micras, "50000" es
	// prefijo de "500000000" y la aserción pasaría con un saldo equivocado.
	if !strings.Contains(raw, `"amount_micros":500000000,`) {
		t.Errorf("el saldo debe ir como entero en micras, se obtuvo: %s", raw)
	}
	if strings.Contains(raw, "500.0") || strings.Contains(raw, "500.00") {
		t.Errorf("el importe viaja como decimal: %s", raw)
	}
}

func TestLosMovimientosPaginanConCursor(t *testing.T) {
	f := setup(t)

	for i := 0; i < 4; i++ {
		if _, err := f.core.Post(f.ctx, uuid.NewString(), "deposit", []coreclient.Entry{
			coreclient.Debit(f.cash, 10_000000, "USD"),
			coreclient.Credit(f.alice.accountID, 10_000000, "USD"),
		}, fmt.Sprintf("depósito %d", i)); err != nil {
			t.Fatalf("depositar: %v", err)
		}
	}

	var page struct {
		Movements  []struct{ Cursor string } `json:"movements"`
		NextCursor string                    `json:"next_cursor"`
	}
	_, body := f.do(t, http.MethodGet,
		"/v1/accounts/"+f.alice.accountID+"/movements?limit=2", f.alice.token, nil, nil)
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatalf("deserializar: %v", err)
	}

	if len(page.Movements) != 2 {
		t.Fatalf("movimientos = %d, se esperaban 2", len(page.Movements))
	}
	if page.NextCursor == "" {
		t.Fatal("debe haber una página siguiente")
	}

	first := page.Movements[0].Cursor
	_, body = f.do(t, http.MethodGet,
		"/v1/accounts/"+f.alice.accountID+"/movements?limit=2&cursor="+page.NextCursor,
		f.alice.token, nil, nil)
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatalf("deserializar: %v", err)
	}
	if len(page.Movements) == 0 {
		t.Fatal("la segunda página no debe venir vacía")
	}
	if page.Movements[0].Cursor == first {
		t.Error("la segunda página repite el primer movimiento")
	}
}

// ---------------------------------------------------------------- transferencias

func TestTransferenciaEntreClientes(t *testing.T) {
	f := setup(t)

	resp, body := f.do(t, http.MethodPost, "/v1/transfers", f.alice.token, map[string]any{
		"from_account_id": f.alice.accountID,
		"to_account_id":   f.bob.accountID,
		"amount_micros":    120_000000,
		"currency":        "USD",
		"description":     "pago",
	}, map[string]string{"Idempotency-Key": uuid.NewString()})

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d: %s", resp.StatusCode, body)
	}

	alice, _ := f.core.GetBalance(f.ctx, f.alice.accountID)
	bob, _ := f.core.GetBalance(f.ctx, f.bob.accountID)
	if alice.AmountMicros != 380_000000 {
		t.Errorf("saldo de origen = %d, se esperaba 38000", alice.AmountMicros)
	}
	if bob.AmountMicros != 220_000000 {
		t.Errorf("saldo de destino = %d, se esperaba 22000", bob.AmountMicros)
	}
}

// Un toque doble en el móvil no puede enviar el dinero dos veces.
func TestElMismoIdempotencyKeyNoTransfiereDosVeces(t *testing.T) {
	f := setup(t)
	key := uuid.NewString()
	payload := map[string]any{
		"from_account_id": f.alice.accountID,
		"to_account_id":   f.bob.accountID,
		"amount_micros":    50_000000,
		"currency":        "USD",
	}

	for i := 0; i < 2; i++ {
		resp, body := f.do(t, http.MethodPost, "/v1/transfers", f.alice.token, payload,
			map[string]string{"Idempotency-Key": key})
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("intento %d: status = %d: %s", i, resp.StatusCode, body)
		}
	}

	balance, _ := f.core.GetBalance(f.ctx, f.alice.accountID)
	if balance.AmountMicros != 450_000000 {
		t.Errorf("saldo = %d, se esperaba 45000: el reenvío transfirió dos veces", balance.AmountMicros)
	}
}

func TestSinIdempotencyKeySeRechazaLaTransferencia(t *testing.T) {
	f := setup(t)

	resp, _ := f.do(t, http.MethodPost, "/v1/transfers", f.alice.token, map[string]any{
		"from_account_id": f.alice.accountID,
		"to_account_id":   f.bob.accountID,
		"amount_micros":    10_000000,
		"currency":        "USD",
	}, nil)

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, se esperaba 400", resp.StatusCode)
	}
}

// Dos clientes distintos pueden usar la misma clave sin interferirse.
func TestLaClaveDeIdempotenciaEstaAisladaPorCliente(t *testing.T) {
	f := setup(t)
	sharedKey := "misma-clave"

	for _, c := range []customer{f.alice, f.bob} {
		resp, body := f.do(t, http.MethodPost, "/v1/transfers", c.token, map[string]any{
			"from_account_id": c.accountID,
			"to_account_id":   f.mallory.accountID,
			"amount_micros":    10_000000,
			"currency":        "USD",
		}, map[string]string{"Idempotency-Key": sharedKey})
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("status = %d: %s", resp.StatusCode, body)
		}
	}

	balance, _ := f.core.GetBalance(f.ctx, f.mallory.accountID)
	if balance.AmountMicros != 20_000000 {
		t.Errorf("destino = %d, se esperaba 2000: la clave de un cliente bloqueó la de otro", balance.AmountMicros)
	}
}

func TestSaldoInsuficienteDevuelveUnMotivoAccionable(t *testing.T) {
	f := setup(t)

	resp, body := f.do(t, http.MethodPost, "/v1/transfers", f.bob.token, map[string]any{
		"from_account_id": f.bob.accountID,
		"to_account_id":   f.alice.accountID,
		"amount_micros":    900_000000,
		"currency":        "USD",
	}, map[string]string{"Idempotency-Key": uuid.NewString()})

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, se esperaba 422", resp.StatusCode)
	}

	var apiErr struct{ Code, Message string }
	if err := json.Unmarshal(body, &apiErr); err != nil {
		t.Fatalf("deserializar: %v", err)
	}
	if apiErr.Code != "insufficient_funds" {
		t.Errorf("código = %q, se esperaba insufficient_funds", apiErr.Code)
	}
	// El detalle interno del core no debe llegar al cliente.
	if strings.Contains(apiErr.Message, "account") || strings.Contains(apiErr.Message, "AB001") {
		t.Errorf("el error filtra detalles internos: %q", apiErr.Message)
	}
}

// Ráfaga concurrente con la misma clave: un móvil con red inestable reintenta.
func TestReintentosConcurrentesConLaMismaClaveTransfierenUnaVez(t *testing.T) {
	f := setup(t)
	key := uuid.NewString()

	const attempts = 6
	var wg sync.WaitGroup
	statuses := make([]int, attempts)
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resp, _ := f.do(t, http.MethodPost, "/v1/transfers", f.alice.token, map[string]any{
				"from_account_id": f.alice.accountID,
				"to_account_id":   f.bob.accountID,
				"amount_micros":    30_000000,
				"currency":        "USD",
			}, map[string]string{"Idempotency-Key": key})
			statuses[i] = resp.StatusCode
		}(i)
	}
	wg.Wait()

	for i, status := range statuses {
		if status != http.StatusCreated {
			t.Errorf("intento %d: status = %d", i, status)
		}
	}
	balance, _ := f.core.GetBalance(f.ctx, f.alice.accountID)
	if balance.AmountMicros != 470_000000 {
		t.Errorf("saldo = %d, se esperaba 47000 pese a %d reintentos", balance.AmountMicros, attempts)
	}
}
