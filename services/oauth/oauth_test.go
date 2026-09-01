package oauth

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/lib/pq"
)

func databaseURL() string {
	if url := os.Getenv("DATABASE_URL"); url != "" {
		return url
	}
	return "postgres://aibank:aibank_dev@localhost:5434/aibank?sslmode=disable"
}

var signingKey = []byte("clave-de-pruebas-de-32-bytes-min!")

type fixture struct {
	srv      *httptest.Server
	store    *Store
	issuer   *Issuer
	server   *Server
	clientID string
	secret   string
}

func setup(t *testing.T) *fixture {
	t.Helper()

	db, err := sql.Open("postgres", databaseURL())
	if err != nil {
		t.Fatalf("abrir base: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	if err := db.PingContext(ctx); err != nil {
		t.Skipf("PostgreSQL no disponible: %v", err)
	}

	store := NewStore(db)
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrar: %v", err)
	}

	issuer, err := NewIssuer("https://api.aibank.test", signingKey, nil)
	if err != nil {
		t.Fatalf("emisor: %v", err)
	}

	clientID := "test_" + uuid.NewString()
	secret, err := store.Register(ctx, clientID, "Integración de prueba",
		[]string{ScopePaymentsRead, ScopePaymentsWrite, ScopeAccountsWrite})
	if err != nil {
		t.Fatalf("registrar: %v", err)
	}

	server := NewServer(store, issuer, nil)
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", server.Token)
	srv := httptest.NewServer(mux)
	srv.Client().Transport = &http.Transport{DisableKeepAlives: true}
	t.Cleanup(srv.Close)

	return &fixture{srv: srv, store: store, issuer: issuer, server: server, clientID: clientID, secret: secret}
}

func (f *fixture) token(t *testing.T, form url.Values) (*http.Response, map[string]any) {
	t.Helper()
	resp, err := f.srv.Client().PostForm(f.srv.URL+"/oauth/token", form)
	if err != nil {
		t.Fatalf("pedir token: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })

	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp, body
}

func (f *fixture) validForm() url.Values {
	return url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {f.clientID},
		"client_secret": {f.secret},
	}
}

// ---------------------------------------------------------------- emisión

func TestSeEmiteUnTokenConCredencialesValidas(t *testing.T) {
	f := setup(t)

	resp, body := f.token(t, f.validForm())

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, se esperaba 200: %v", resp.StatusCode, body)
	}
	if body["token_type"] != "Bearer" {
		t.Errorf("token_type = %v", body["token_type"])
	}
	if body["expires_in"] != float64(3600) {
		t.Errorf("expires_in = %v, se esperaba 3600", body["expires_in"])
	}
	if _, ok := body["access_token"].(string); !ok {
		t.Fatal("falta access_token")
	}
}

func TestUnTokenNuncaSeCachea(t *testing.T) {
	f := setup(t)

	resp, _ := f.token(t, f.validForm())

	// Un proxy intermedio que guarde esta respuesta se la entrega al siguiente
	// que pregunte.
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, se esperaba no-store", got)
	}
}

func TestLasCredencialesTambienViajanPorBasicAuth(t *testing.T) {
	f := setup(t)

	req, _ := http.NewRequest(http.MethodPost, f.srv.URL+"/oauth/token",
		strings.NewReader("grant_type=client_credentials"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(f.clientID, f.secret)

	resp, err := f.srv.Client().Do(req)
	if err != nil {
		t.Fatalf("pedir token: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d con Basic auth, se esperaba 200", resp.StatusCode)
	}
}

// ---------------------------------------------------------------- rechazos

func TestUnSecretoIncorrectoNoRevelaSiElClienteExiste(t *testing.T) {
	f := setup(t)

	form := f.validForm()
	form.Set("client_secret", "incorrecto")
	badSecret, badSecretBody := f.token(t, form)

	form = f.validForm()
	form.Set("client_id", "no_existe_"+uuid.NewString())
	unknown, unknownBody := f.token(t, form)

	// Si el cliente inexistente respondiera distinto, el endpoint sería un oráculo
	// para descubrir qué client_id están dados de alta.
	if badSecret.StatusCode != unknown.StatusCode {
		t.Errorf("status distinto: secreto malo = %d, cliente inexistente = %d",
			badSecret.StatusCode, unknown.StatusCode)
	}
	if badSecretBody["error"] != unknownBody["error"] {
		t.Errorf("error distinto: %v vs %v", badSecretBody["error"], unknownBody["error"])
	}
	if badSecretBody["error_description"] != unknownBody["error_description"] {
		t.Errorf("descripción distinta: %v vs %v",
			badSecretBody["error_description"], unknownBody["error_description"])
	}
	if badSecret.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, se esperaba 401", badSecret.StatusCode)
	}
}

func TestOtroGrantTypeSeRechaza(t *testing.T) {
	f := setup(t)

	form := f.validForm()
	form.Set("grant_type", "password")
	resp, body := f.token(t, form)

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, se esperaba 400", resp.StatusCode)
	}
	if body["error"] != "unsupported_grant_type" {
		t.Errorf("error = %v", body["error"])
	}
}

func TestPedirUnScopeNoConcedidoEsUnError(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	limitado := "solo_lectura_" + uuid.NewString()
	secret, err := f.store.Register(ctx, limitado, "Panel de monitoreo", []string{ScopePaymentsRead})
	if err != nil {
		t.Fatalf("registrar: %v", err)
	}

	resp, body := f.token(t, url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {limitado},
		"client_secret": {secret},
		"scope":         {"payments:read payments:write"},
	})

	// Recortar el scope en silencio le daría al cliente un token de solo lectura
	// creyendo que puede mover dinero; se enteraría al intentar cobrar.
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, se esperaba 400", resp.StatusCode)
	}
	if body["error"] != "invalid_scope" {
		t.Errorf("error = %v, se esperaba invalid_scope", body["error"])
	}
}

func TestSinScopePedidoSeConcedenLosDelCliente(t *testing.T) {
	f := setup(t)

	_, body := f.token(t, f.validForm())

	scope, _ := body["scope"].(string)
	for _, want := range []string{ScopePaymentsRead, ScopePaymentsWrite, ScopeAccountsWrite} {
		if !strings.Contains(scope, want) {
			t.Errorf("scope = %q, falta %s", scope, want)
		}
	}
}

func TestUnClienteDesactivadoNoObtieneToken(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	db, _ := sql.Open("postgres", databaseURL())
	defer db.Close()
	if _, err := db.ExecContext(ctx,
		"UPDATE oauth.clients SET active = false WHERE client_id = $1", f.clientID); err != nil {
		t.Fatalf("desactivar: %v", err)
	}

	resp, _ := f.token(t, f.validForm())
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, se esperaba 401", resp.StatusCode)
	}
}

// ---------------------------------------------------------------- rotación

func TestTrasRotarSirvenElSecretoNuevoYElAnterior(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	nuevo, err := f.store.RotateSecret(ctx, f.clientID)
	if err != nil {
		t.Fatalf("rotar: %v", err)
	}
	if nuevo == f.secret {
		t.Fatal("la rotación devolvió el mismo secreto")
	}

	// Los dos tienen que servir durante la ventana de gracia: si el anterior
	// muriera al instante, rotar exigiría desplegar los dos lados a la vez, que es
	// justo la coordinación que hace que las rotaciones no se hagan nunca.
	form := f.validForm()
	form.Set("client_secret", nuevo)
	if resp, _ := f.token(t, form); resp.StatusCode != http.StatusOK {
		t.Errorf("el secreto nuevo no sirve: %d", resp.StatusCode)
	}
	if resp, _ := f.token(t, f.validForm()); resp.StatusCode != http.StatusOK {
		t.Errorf("el secreto anterior dejó de servir en la ventana de gracia: %d", resp.StatusCode)
	}
}

func TestElSecretoAnteriorMuereAlVencerLaVentana(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	if _, err := f.store.RotateSecret(ctx, f.clientID); err != nil {
		t.Fatalf("rotar: %v", err)
	}

	db, _ := sql.Open("postgres", databaseURL())
	defer db.Close()
	if _, err := db.ExecContext(ctx,
		"UPDATE oauth.clients SET previous_expires_at = now() - interval '1 minute' WHERE client_id = $1",
		f.clientID); err != nil {
		t.Fatalf("vencer la ventana: %v", err)
	}

	if resp, _ := f.token(t, f.validForm()); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("el secreto anterior sigue sirviendo tras la ventana: %d", resp.StatusCode)
	}
}

func TestRotarUnClienteInexistenteFalla(t *testing.T) {
	f := setup(t)
	if _, err := f.store.RotateSecret(context.Background(), "no_existe"); err != ErrClientNotFound {
		t.Errorf("error = %v, se esperaba ErrClientNotFound", err)
	}
}

// ---------------------------------------------------------------- tokens

func TestUnTokenValidoSeVerificaYTraeSusScopes(t *testing.T) {
	f := setup(t)
	_, body := f.token(t, f.validForm())

	claims, err := f.issuer.Verify(body["access_token"].(string))
	if err != nil {
		t.Fatalf("verificar: %v", err)
	}
	if claims.Subject != f.clientID {
		t.Errorf("sub = %q", claims.Subject)
	}
	if !claims.HasScope(ScopePaymentsWrite) {
		t.Error("falta el scope payments:write")
	}
	if claims.TokenID == "" {
		t.Error("el token no lleva jti: sin él no se puede revocar ni rastrear uno concreto")
	}
}

func TestUnTokenFirmadoConOtraClaveSeRechaza(t *testing.T) {
	f := setup(t)
	_, body := f.token(t, f.validForm())
	token := body["access_token"].(string)

	otro, _ := NewIssuer("https://api.aibank.test", []byte("otra-clave-de-32-bytes-para-test"), nil)
	if _, err := otro.Verify(token); err != ErrTokenSignature {
		t.Errorf("error = %v, se esperaba ErrTokenSignature", err)
	}
}

// El ataque clásico contra JWT: cambiar el algoritmo a "none" y quitar la firma.
// Solo funciona contra verificadores que le preguntan al token cómo verificarse.
func TestUnTokenConAlgNoneSeRechaza(t *testing.T) {
	f := setup(t)
	_, body := f.token(t, f.validForm())
	parts := strings.Split(body["access_token"].(string), ".")

	forjado := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`)) +
		"." + parts[1] + "."

	if _, err := f.issuer.Verify(forjado); err != ErrTokenMalformed {
		t.Errorf("error = %v, se esperaba ErrTokenMalformed — alg:none no puede llegar ni a la firma", err)
	}
}

func TestUnPayloadAlteradoInvalidaLaFirma(t *testing.T) {
	f := setup(t)
	_, body := f.token(t, f.validForm())
	parts := strings.Split(body["access_token"].(string), ".")

	// Se sube el scope a mano, que es exactamente lo que intentaría alguien con
	// un token de solo lectura.
	alterado, _ := json.Marshal(Claims{
		Issuer: "https://api.aibank.test", Subject: f.clientID,
		Scopes:    []string{ScopePaymentsWrite},
		IssuedAt:  time.Now().Unix(),
		ExpiresAt: time.Now().Add(time.Hour).Unix(),
		TokenID:   uuid.NewString(),
	})
	forjado := parts[0] + "." + base64.RawURLEncoding.EncodeToString(alterado) + "." + parts[2]

	if _, err := f.issuer.Verify(forjado); err != ErrTokenSignature {
		t.Errorf("error = %v, se esperaba ErrTokenSignature", err)
	}
}

func TestUnTokenVencidoSeDistingueDeUnoInvalido(t *testing.T) {
	f := setup(t)

	pasado, _ := NewIssuer("https://api.aibank.test", signingKey, nil)
	pasado.now = func() time.Time { return time.Now().Add(-2 * time.Hour) }
	token, _, _ := pasado.Issue(f.clientID, []string{ScopePaymentsRead}, uuid.NewString())

	// La distinción no filtra nada y le dice al cliente que pida otro token en vez
	// de revisar sus credenciales.
	if _, err := f.issuer.Verify(token); err != ErrTokenExpired {
		t.Errorf("error = %v, se esperaba ErrTokenExpired", err)
	}
}

func TestUnTokenDeOtroEmisorSeRechaza(t *testing.T) {
	f := setup(t)

	// Misma clave de firma, distinto emisor: es el caso de dos entornos del banco
	// que comparten secreto por error. Un token de sandbox no puede valer en
	// producción.
	sandbox, _ := NewIssuer("https://sandbox.aibank.test", signingKey, nil)
	token, _, _ := sandbox.Issue(f.clientID, []string{ScopePaymentsWrite}, uuid.NewString())

	if _, err := f.issuer.Verify(token); err != ErrTokenSignature {
		t.Errorf("error = %v, se esperaba rechazo por emisor distinto", err)
	}
}

func TestSeAceptanTokensFirmadosConLaClaveAnterior(t *testing.T) {
	f := setup(t)

	vieja := []byte("clave-anterior-de-32-bytes-larga")
	emisorViejo, _ := NewIssuer("https://api.aibank.test", vieja, nil)
	token, _, _ := emisorViejo.Issue(f.clientID, []string{ScopePaymentsRead}, uuid.NewString())

	// Rotar la clave de firma no puede invalidar de golpe los tokens en vuelo.
	rotado, _ := NewIssuer("https://api.aibank.test", signingKey, vieja)
	if _, err := rotado.Verify(token); err != nil {
		t.Errorf("un token de la clave anterior no se aceptó: %v", err)
	}
}

func TestUnaClaveDeFirmaCortaSeRechazaAlArrancar(t *testing.T) {
	// Falla al construir el emisor, no al primer token: un servicio con una clave
	// débil no debe llegar a atender tráfico.
	if _, err := NewIssuer("https://api.aibank.test", []byte("corta"), nil); err == nil {
		t.Error("se aceptó una clave de firma de 5 bytes")
	}
}

func TestUnTokenMalformadoNoRompe(t *testing.T) {
	f := setup(t)
	for _, token := range []string{"", "abc", "a.b", "a.b.c.d", "....", "no-es-base64.$$$.zz"} {
		if _, err := f.issuer.Verify(token); err == nil {
			t.Errorf("se aceptó el token malformado %q", token)
		}
	}
}

// ---------------------------------------------------------------- middleware

func TestAuthenticateExigeElScope(t *testing.T) {
	f := setup(t)
	token, _, _ := f.issuer.Issue(f.clientID, []string{ScopePaymentsRead}, uuid.NewString())

	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	if _, authErr := f.server.Authenticate(req, ScopePaymentsRead); authErr != nil {
		t.Errorf("un token con el scope correcto fue rechazado: %v", authErr)
	}

	_, authErr := f.server.Authenticate(req, ScopePaymentsWrite)
	if authErr == nil {
		t.Fatal("un token de solo lectura autorizó una escritura")
	}
	// 403 y no 401: el token es válido, lo que falta es permiso. Un 401 haría que
	// el cliente pidiera otro token en un bucle que nunca resuelve nada.
	if authErr.Status != http.StatusForbidden {
		t.Errorf("status = %d, se esperaba 403", authErr.Status)
	}
	if authErr.Code != "insufficient_scope" {
		t.Errorf("code = %q", authErr.Code)
	}
}

func TestSinEncabezadoAuthorizationSeRechaza(t *testing.T) {
	f := setup(t)
	req, _ := http.NewRequest(http.MethodGet, "/", nil)

	_, authErr := f.server.Authenticate(req, ScopePaymentsRead)
	if authErr == nil || authErr.Status != http.StatusUnauthorized {
		t.Fatalf("se esperaba 401, fue: %v", authErr)
	}
	if authErr.Code != "invalid_credential" {
		t.Errorf("code = %q, se esperaba invalid_credential", authErr.Code)
	}
}

// ---------------------------------------------------------------- sub-cuentas

func TestEnlazarLaMismaSubCuentaDosVecesDevuelveLaPrimera(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	sub := SubAccount{
		AccountID:      uuid.NewString(),
		OwnerReference: "agent_7c1e9a",
		Currency:       "USD",
		DisplayName:    "research-bot-42",
	}

	first, existed, err := f.store.LinkAccount(ctx, f.clientID, sub)
	if err != nil || existed {
		t.Fatalf("primer enlace: %v (existía: %v)", err, existed)
	}

	// Un reintento tras un timeout no puede abrir una segunda cuenta y partir el
	// saldo del agente en dos.
	segundo := sub
	segundo.AccountID = uuid.NewString()
	again, existed, err := f.store.LinkAccount(ctx, f.clientID, segundo)
	if err != nil {
		t.Fatalf("segundo enlace: %v", err)
	}
	if !existed {
		t.Error("el segundo enlace creó una cuenta nueva")
	}
	if again.AccountID != first.AccountID {
		t.Errorf("devolvió otra cuenta: %s vs %s", again.AccountID, first.AccountID)
	}
}

func TestLaMismaReferenciaEnOtraMonedaEsOtraCuenta(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	ref := "agent_" + uuid.NewString()

	usd, _, _ := f.store.LinkAccount(ctx, f.clientID,
		SubAccount{AccountID: uuid.NewString(), OwnerReference: ref, Currency: "USD"})
	pen, existed, err := f.store.LinkAccount(ctx, f.clientID,
		SubAccount{AccountID: uuid.NewString(), OwnerReference: ref, Currency: "PEN"})
	if err != nil {
		t.Fatalf("enlazar PEN: %v", err)
	}

	// Las cuentas son mono-moneda: un agente que opera en dos monedas tiene dos.
	if existed || pen.AccountID == usd.AccountID {
		t.Error("la cuenta en PEN colisionó con la de USD")
	}
}

func TestUnaCuentaAjenaNoPerteneceALaIntegracion(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	propia := uuid.NewString()
	if _, _, err := f.store.LinkAccount(ctx, f.clientID, SubAccount{
		AccountID: propia, OwnerReference: "agent_propio", Currency: "USD",
	}); err != nil {
		t.Fatalf("enlazar: %v", err)
	}

	if ok, _ := f.store.OwnsAccount(ctx, f.clientID, propia); !ok {
		t.Error("no reconoció su propia cuenta")
	}

	// La barrera que impide mover el dinero de una cuenta ajena conociendo su id.
	if ok, _ := f.store.OwnsAccount(ctx, f.clientID, uuid.NewString()); ok {
		t.Error("reclamó como propia una cuenta que no enlazó")
	}
	// Un identificador que ni siquiera es un UUID no puede hacer fallar la consulta.
	if ok, err := f.store.OwnsAccount(ctx, f.clientID, "'; DROP TABLE oauth.clients; --"); ok || err != nil {
		t.Errorf("un id inválido dio ok=%v err=%v", ok, err)
	}
}

func TestElSecretoNoSeGuardaEnClaro(t *testing.T) {
	f := setup(t)

	db, _ := sql.Open("postgres", databaseURL())
	defer db.Close()

	var stored string
	if err := db.QueryRow("SELECT secret_hash FROM oauth.clients WHERE client_id = $1",
		f.clientID).Scan(&stored); err != nil {
		t.Fatalf("leer: %v", err)
	}

	if strings.Contains(stored, f.secret) {
		t.Fatal("el secreto quedó guardado en claro")
	}
	if len(stored) != 64 {
		t.Errorf("el hash mide %d caracteres, se esperaban 64 (SHA-256 en hex)", len(stored))
	}
}
