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
	"database/sql"
	"encoding/json"
	"errors"
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
	"github.com/aibank/aibank/services/oauth"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
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
		"amount_micros":   100_000000,
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
		"amount_micros":   10_000000,
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
			Currency     string `json:"currency"`
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
		"amount_micros":   120_000000,
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
		"amount_micros":   50_000000,
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
		"amount_micros":   10_000000,
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
			"amount_micros":   10_000000,
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
		"amount_micros":   900_000000,
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
				"amount_micros":   30_000000,
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

// ---------------------------------------------------------------- lo retenido

type homeHolds struct {
	Balance struct {
		AmountMicros int64 `json:"amount_micros"`
	} `json:"balance"`
	Retained *struct {
		AmountMicros int64  `json:"amount_micros"`
		Currency     string `json:"currency"`
	} `json:"retained"`
	Pending []struct {
		Merchant string `json:"merchant"`
		Count    int64  `json:"count"`
		Amount   struct {
			AmountMicros int64 `json:"amount_micros"`
		} `json:"amount"`
		Expired struct {
			AmountMicros int64 `json:"amount_micros"`
		} `json:"expired"`
		LastExpiresAt time.Time `json:"last_expires_at"`
	} `json:"pending"`
}

// payUnderMandate le da a una plataforma permiso sobre la cuenta de Alice y paga
// con él a Bob: lo que hace un agente en el modelo B.
func (f *fixture) payUnderMandate(t *testing.T, grantee string, amounts ...int64) {
	t.Helper()

	// El core necesita la cuenta de retenciones de la moneda para autorizar.
	if _, err := f.core.CreateInternalAccount(f.ctx, "AUTH-HOLDS-USD", "Retenciones",
		corev1.AccountType_ACCOUNT_TYPE_LIABILITY, "USD"); err != nil &&
		!errors.Is(err, coreclient.ErrAlreadyExists) {
		t.Fatalf("cuenta de retenciones: %v", err)
	}

	mandate, err := f.core.GrantMandate(f.ctx, f.alice.accountID, grantee, f.alice.customerID,
		"consent:test", nil, nil, time.Now().Add(30*24*time.Hour))
	if err != nil {
		t.Fatalf("otorgar mandato: %v", err)
	}
	for _, amount := range amounts {
		if _, _, err := f.core.AuthorizeUnderMandate(f.ctx, mandate.ID, uuid.NewString(),
			f.alice.accountID, f.bob.accountID, amount, "USD"); err != nil {
			t.Fatalf("autorizar: %v", err)
		}
	}
}

func getHomeHolds(t *testing.T, f *fixture, base string) homeHolds {
	t.Helper()
	req, _ := http.NewRequestWithContext(f.ctx, http.MethodGet,
		base+"/v1/home?account_id="+f.alice.accountID, nil)
	req.Header.Set("Authorization", "Bearer "+f.alice.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("home: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("home: status %d", resp.StatusCode)
	}
	var home homeHolds
	if err := json.NewDecoder(resp.Body).Decode(&home); err != nil {
		t.Fatalf("deserializar: %v", err)
	}
	return home
}

// "Cero retenido" es una afirmación, y se hace explícita: la app no tiene que
// deducirla de que falte un campo.
func TestSinPagosEnCursoElInicioDiceCeroRetenido(t *testing.T) {
	f := setup(t)
	home := getHomeHolds(t, f, f.srv.URL)

	if home.Retained == nil {
		t.Fatal("falta retained: la app no puede distinguir cero de desconocido")
	}
	if home.Retained.AmountMicros != 0 || len(home.Pending) != 0 {
		t.Errorf("retenido = %d con %d grupos; se esperaba nada", home.Retained.AmountMicros, len(home.Pending))
	}
}

func TestElInicioMuestraLoRetenidoAgrupadoPorPlataforma(t *testing.T) {
	f := setup(t)
	grantee := "plataforma-" + uuid.NewString()[:8]
	f.payUnderMandate(t, grantee, 1_500000, 2_000000)

	home := getHomeHolds(t, f, f.srv.URL)

	// El saldo ya no incluye lo retenido: es lo disponible.
	if home.Balance.AmountMicros != 500_000000-3_500000 {
		t.Errorf("disponible = %d", home.Balance.AmountMicros)
	}
	if home.Retained == nil || home.Retained.AmountMicros != 3_500000 {
		t.Fatalf("retenido = %+v, se esperaba 3,50", home.Retained)
	}
	if len(home.Pending) != 1 {
		t.Fatalf("grupos = %d, se esperaba uno: los dos pagos son de la misma plataforma", len(home.Pending))
	}
	g := home.Pending[0]
	// Sin registro de integraciones montado se ve el client_id: menos amable,
	// pero sigue identificando de verdad a quien cobra.
	if g.Merchant != grantee || g.Count != 2 || g.Amount.AmountMicros != 3_500000 {
		t.Errorf("grupo = %+v", g)
	}
	if g.Expired.AmountMicros != 0 {
		t.Errorf("vencido = %d, se esperaba 0", g.Expired.AmountMicros)
	}
	if time.Until(g.LastExpiresAt) <= 0 {
		t.Errorf("last_expires_at = %v, tendría que estar en el futuro", g.LastExpiresAt)
	}
}

// El nombre que ve el titular viene del registro del banco, no de lo que la
// plataforma diga de sí misma: el mismo criterio que la pantalla donde concedió
// el permiso.
func TestLaPlataformaApareceConSuNombreRegistrado(t *testing.T) {
	f := setup(t)

	db, err := sql.Open("postgres", bffDatabaseURL())
	if err != nil {
		t.Fatalf("abrir base: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := oauth.NewStore(db)
	if err := store.Migrate(f.ctx); err != nil {
		t.Skipf("base no disponible: %v", err)
	}

	grantee := "plataforma-" + uuid.NewString()[:8]
	if _, err := store.Register(f.ctx, grantee, "Plataforma de Prueba", []string{oauth.ScopePaymentsWrite}); err != nil {
		t.Fatalf("registrar integración: %v", err)
	}
	f.payUnderMandate(t, grantee, 1_000000)

	withRegistry := httptest.NewServer(bff.NewServer(f.core, f.auth, nil).WithConsents(store).Handler())
	t.Cleanup(withRegistry.Close)

	home := getHomeHolds(t, f, withRegistry.URL)
	if len(home.Pending) != 1 || home.Pending[0].Merchant != "Plataforma de Prueba" {
		t.Errorf("pending = %+v, se esperaba el nombre registrado", home.Pending)
	}
}

func bffDatabaseURL() string {
	if u := os.Getenv("DATABASE_URL"); u != "" {
		return u
	}
	return "postgres://aibank:aibank_dev@localhost:5434/aibank?sslmode=disable"
}

// ---------------------------------------------------------------- centro de control

// serverWithRegistry monta el canal con el registro de integraciones, que es lo
// que habilita las rutas de permisos y los nombres registrados.
func serverWithRegistry(t *testing.T, f *fixture) (*httptest.Server, *oauth.Store) {
	t.Helper()
	db, err := sql.Open("postgres", bffDatabaseURL())
	if err != nil {
		t.Fatalf("abrir base: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := oauth.NewStore(db)
	if err := store.Migrate(f.ctx); err != nil {
		t.Skipf("base no disponible: %v", err)
	}
	ts := httptest.NewServer(bff.NewServer(f.core, f.auth, nil).WithConsents(store).Handler())
	t.Cleanup(ts.Close)
	return ts, store
}

func getJSON(t *testing.T, f *fixture, url string, out any) {
	t.Helper()
	req, _ := http.NewRequestWithContext(f.ctx, http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer "+f.alice.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d", url, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatal(err)
	}
}

// Sin la plataforma, el historial de un agente son cien filas anónimas de
// "Pago autorizado". Con ella, la app puede agruparlas.
func TestCadaPagoDeUnAgenteLlevaSuPlataformaEnElHistorial(t *testing.T) {
	f := setup(t)
	grantee := "plataforma-" + uuid.NewString()[:8]
	f.payUnderMandate(t, grantee, 1_000, 2_000, 3_000)

	var home struct {
		Movements []struct {
			Kind     string `json:"kind"`
			Merchant string `json:"merchant"`
		} `json:"movements"`
	}
	getJSON(t, f, f.srv.URL+"/v1/home?account_id="+f.alice.accountID, &home)

	var pagos, otros int
	for _, m := range home.Movements {
		switch m.Kind {
		case "authorization_hold":
			pagos++
			if m.Merchant != grantee {
				t.Errorf("pago sin su plataforma: %+v", m)
			}
		default:
			otros++
			// El depósito inicial no es un pago a nadie: no se le inventa una.
			if m.Merchant != "" {
				t.Errorf("movimiento %s con plataforma inventada: %q", m.Kind, m.Merchant)
			}
		}
	}
	if pagos != 3 || otros == 0 {
		t.Errorf("pagos = %d, otros = %d", pagos, otros)
	}
}

// La persona concedió el permiso a "Mercatus", no a "mercatus_sandbox". Por ese
// nombre lo va a buscar para retirarlo, y por ese nombre tiene que verlo.
func TestLosPermisosYLosPagosUsanElNombreRegistrado(t *testing.T) {
	f := setup(t)
	srv, store := serverWithRegistry(t, f)

	grantee := "plataforma-" + uuid.NewString()[:8]
	if _, err := store.Register(f.ctx, grantee, "Plataforma de Prueba", []string{oauth.ScopePaymentsWrite}); err != nil {
		t.Fatal(err)
	}
	f.payUnderMandate(t, grantee, 1_000)

	var mandates struct {
		Mandates []struct {
			GrantedTo string `json:"granted_to"`
		} `json:"mandates"`
	}
	getJSON(t, f, srv.URL+"/v1/mandates", &mandates)
	if len(mandates.Mandates) != 1 || mandates.Mandates[0].GrantedTo != "Plataforma de Prueba" {
		t.Errorf("permisos = %+v; se esperaba el nombre registrado, no %q", mandates.Mandates, grantee)
	}

	var home struct {
		Movements []struct {
			Kind     string `json:"kind"`
			Merchant string `json:"merchant"`
		} `json:"movements"`
	}
	getJSON(t, f, srv.URL+"/v1/home?account_id="+f.alice.accountID, &home)
	for _, m := range home.Movements {
		if m.Kind == "authorization_hold" && m.Merchant != "Plataforma de Prueba" {
			t.Errorf("pago con %q, se esperaba el nombre registrado", m.Merchant)
		}
	}
}

func TestCadaPermisoTraeLoGastadoHoyYEsteMes(t *testing.T) {
	f := setup(t)
	srv, _ := serverWithRegistry(t, f)
	f.payUnderMandate(t, "plataforma-"+uuid.NewString()[:8], 1_000, 2_000, 500_000)

	var mandates struct {
		Mandates []struct {
			Spend *struct {
				Today struct {
					AmountMicros int64 `json:"amount_micros"`
				} `json:"today"`
				TodayCount int64 `json:"today_count"`
				Month      struct {
					AmountMicros int64 `json:"amount_micros"`
				} `json:"month"`
				InFlight struct {
					AmountMicros int64 `json:"amount_micros"`
				} `json:"in_flight"`
			} `json:"spend"`
		} `json:"mandates"`
	}
	getJSON(t, f, srv.URL+"/v1/mandates", &mandates)

	sp := mandates.Mandates[0].Spend
	if sp == nil {
		t.Fatal("falta el gasto del permiso")
	}
	if sp.Today.AmountMicros != 503_000 || sp.TodayCount != 3 {
		t.Errorf("hoy = %d en %d pagos; se esperaba 503000 en 3", sp.Today.AmountMicros, sp.TodayCount)
	}
	// Nada se cobró todavía: todo está en curso, y el mes incluye lo de hoy.
	if sp.InFlight.AmountMicros != 503_000 || sp.Month.AmountMicros != 503_000 {
		t.Errorf("en curso = %d, mes = %d", sp.InFlight.AmountMicros, sp.Month.AmountMicros)
	}
}

// La pantalla de inicio trae los últimos movimientos, no todos. La app necesita
// saber si hay más atrás para no rotular "10 pagos" lo que fueron 121.
func TestElInicioDiceSiHayMovimientosMasAntiguos(t *testing.T) {
	f := setup(t)

	var poco struct {
		HasMore bool `json:"has_more"`
	}
	getJSON(t, f, f.srv.URL+"/v1/home?account_id="+f.alice.accountID, &poco)
	if poco.HasMore {
		t.Error("con un solo movimiento dice que hay más")
	}

	pagos := make([]int64, 12)
	for i := range pagos {
		pagos[i] = 1_000
	}
	f.payUnderMandate(t, "plataforma-"+uuid.NewString()[:8], pagos...)

	var mucho struct {
		HasMore   bool              `json:"has_more"`
		Movements []json.RawMessage `json:"movements"`
	}
	getJSON(t, f, f.srv.URL+"/v1/home?account_id="+f.alice.accountID, &mucho)
	if !mucho.HasMore {
		t.Errorf("con 13 movimientos y %d en pantalla, no dice que hay más", len(mucho.Movements))
	}
}
