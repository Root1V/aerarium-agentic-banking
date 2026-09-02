// Recorrido completo del riel de pago, por HTTP y contra el sandbox real.
//
// No es una prueba: es el cliente que un socio escribiría, ejecutando el camino
// entero y narrando cada paso. Sirve para tres cosas —comprobar que un sandbox
// recién levantado funciona, mostrar el orden de las llamadas del modelo B (que
// es asíncrono y no se adivina leyendo el OpenAPI), y tener un ejemplo que se
// rompe solo si el contrato cambia.
//
//	go run ./services/mercatus/cmd/demo -model b \
//	  -client-id mercatus_sandbox -client-secret "$SECRET"
//
// El modelo A no necesita el canal del titular: la cuenta es de la plataforma.
// El modelo B sí, porque la mitad del flujo la protagoniza una persona.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

func main() {
	var (
		partnerURL = flag.String("partner-url", env("PARTNER_URL", "http://localhost:8081"),
			"API de socio")
		holderURL = flag.String("holder-url", env("HOLDER_URL", "http://localhost:8080"),
			"canal del titular (solo modelo B)")
		clientID     = flag.String("client-id", env("CLIENT_ID", "mercatus_sandbox"), "credencial de la integración")
		clientSecret = flag.String("client-secret", env("CLIENT_SECRET", ""), "secreto de la integración")
		model        = flag.String("model", "b", "modelo a ejercitar: a (ómnibus) o b (cuenta del titular)")
		amount       = flag.Int64("amount", 1000, "importe del pago, en micras")
		currency     = flag.String("currency", "USD", "moneda")
	)
	flag.Parse()

	if *clientSecret == "" {
		fmt.Fprintln(os.Stderr, "falta -client-secret (lo imprime el alta de la integración)")
		os.Exit(2)
	}

	demo := &demo{
		partner:  &api{base: strings.TrimRight(*partnerURL, "/")},
		holder:   &api{base: strings.TrimRight(*holderURL, "/")},
		currency: strings.ToUpper(*currency),
		amount:   *amount,
	}

	var err error
	switch strings.ToLower(*model) {
	case "a":
		err = demo.modelA(*clientID, *clientSecret)
	case "b":
		err = demo.modelB(*clientID, *clientSecret)
	default:
		err = fmt.Errorf("modelo desconocido: %q (a o b)", *model)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n✗ %v\n", err)
		os.Exit(1)
	}
	fmt.Println("\n✓ recorrido completo")
}

type demo struct {
	partner  *api
	holder   *api
	currency string
	amount   int64
	// steps numera los pasos según se ejecutan. El modelo A salta los tres del
	// consentimiento, y numerarlos a mano dejaría huecos en su salida.
	steps int
}

// ------------------------------------------------------------------ modelo A

func (d *demo) modelA(clientID, clientSecret string) error {
	title("Modelo A — la cuenta pagadora es de la plataforma")

	if err := d.authenticate(clientID, clientSecret); err != nil {
		return err
	}

	payer, err := d.openAgentAccount("agent-demo-payer", 50_000_000)
	if err != nil {
		return err
	}
	payee, err := d.openAgentAccount("agent-demo-payee", 0)
	if err != nil {
		return err
	}

	auth, err := d.authorize(payer, payee, "")
	if err != nil {
		return err
	}
	return d.settle(auth)
}

// ------------------------------------------------------------------ modelo B

func (d *demo) modelB(clientID, clientSecret string) error {
	title("Modelo B — la cuenta pagadora es del cliente del banco")

	if err := d.authenticate(clientID, clientSecret); err != nil {
		return err
	}

	// El receptor sigue siendo una cuenta de la plataforma: lo que cambia es de
	// dónde sale el dinero, no a dónde llega.
	payee, err := d.openAgentAccount("agent-demo-payee", 0)
	if err != nil {
		return err
	}

	holder, err := d.createSandboxHolder()
	if err != nil {
		return err
	}

	request, err := d.requestConsent()
	if err != nil {
		return err
	}

	if err := d.holderApproves(holder, request.HandoffCode); err != nil {
		return err
	}

	granted, err := d.pollConsent(request.ConsentRequestID)
	if err != nil {
		return err
	}

	auth, err := d.authorize(granted.AccountID, payee, granted.MandateID)
	if err != nil {
		return err
	}
	if err := d.settle(auth); err != nil {
		return err
	}

	return d.showMandate(granted.MandateID)
}

// ------------------------------------------------------------------- pasos

func (d *demo) authenticate(clientID, clientSecret string) error {
	d.step("Token de la integración (client_credentials)")

	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
	}
	var token struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		Scope       string `json:"scope"`
	}
	if err := d.partner.form("POST", "/oauth/token", form, &token); err != nil {
		return err
	}

	d.partner.token = token.AccessToken
	say("scopes: %s · vive %d s", token.Scope, token.ExpiresIn)
	return nil
}

type accountResponse struct {
	AccountID string `json:"account_id"`
	Currency  string `json:"currency"`
	Status    string `json:"status"`
}

func (d *demo) openAgentAccount(reference string, initial int64) (string, error) {
	d.step("Sub-cuenta del agente %q", reference)

	body := map[string]any{
		"owner_reference": reference,
		"currency":        d.currency,
		"display_name":    "Agente de demostración",
	}
	if initial > 0 {
		body["initial_balance"] = initial
	}

	var account accountResponse
	// La apertura es idempotente por owner_reference + moneda: repetir la demo
	// no parte el saldo del agente en dos cuentas.
	if err := d.partner.json("POST", "/v1/accounts", body, &account); err != nil {
		return "", err
	}
	say("%s · %s", account.AccountID, account.Status)
	return account.AccountID, nil
}

type sandboxHolder struct {
	CustomerID string `json:"customer_id"`
	AccountID  string `json:"account_id"`
	Token      string `json:"token"`
}

// createSandboxHolder invoca al simulador de titulares del canal del cliente.
//
// En producción esta persona llega por el onboarding, con KYC, y no hay atajo.
// Aquí hace falta uno: sin un titular no hay quien conceda el permiso, y el
// modelo B no se podría probar en absoluto.
func (d *demo) createSandboxHolder() (*sandboxHolder, error) {
	d.step("Un titular de prueba con saldo (solo existe en el sandbox)")

	var holder sandboxHolder
	if err := d.holder.json("POST", "/dev/holders", map[string]any{
		"currency":        d.currency,
		"name":            "Ana Pérez",
		"initial_balance": 50_000_000,
	}, &holder); err != nil {
		return nil, fmt.Errorf("crear titular de prueba: %w "+
			"(¿está arriba el canal del titular con ALLOW_DEV_AUTH=true?)", err)
	}
	say("cuenta %s", holder.AccountID)
	return &holder, nil
}

type consentRequest struct {
	ConsentRequestID string    `json:"consent_request_id"`
	HandoffCode      string    `json:"handoff_code"`
	Status           string    `json:"status"`
	ExpiresAt        time.Time `json:"expires_at"`
	MandateID        string    `json:"mandate_id"`
	AccountID        string    `json:"account_id"`
}

func (d *demo) requestConsent() (*consentRequest, error) {
	d.step("La plataforma pide permiso — sin nombrar ninguna cuenta")

	perOperation := int64(5_000_000)
	total := int64(50_000_000)

	var request consentRequest
	if err := d.partner.json("POST", "/v1/consent-requests", map[string]any{
		"currency":          d.currency,
		"max_per_operation": perOperation,
		"max_total":         total,
		"purpose":           "Pagos del agente de investigación de Acme S.A.",
	}, &request); err != nil {
		return nil, err
	}

	say("handoff_code: %s", request.HandoffCode)
	say("se lo entregas al cliente como enlace o QR; vive hasta %s",
		request.ExpiresAt.Format(time.RFC3339))
	return &request, nil
}

func (d *demo) holderApproves(holder *sandboxHolder, handoff string) error {
	d.step("El titular abre su app del banco y decide")

	d.holder.token = holder.Token

	var prompt struct {
		RequestedBy     string `json:"requested_by"`
		Purpose         string `json:"purpose"`
		MaxPerOperation *int64 `json:"max_per_operation"`
		MaxTotal        *int64 `json:"max_total"`
	}
	if err := d.holder.json("GET", "/v1/consent-requests/"+handoff, nil, &prompt); err != nil {
		return err
	}
	say("ve: %q pide %q", prompt.RequestedBy, prompt.Purpose)

	// Concede la MITAD de lo que le piden por operación. Es el caso interesante:
	// el permiso vale por lo que decidió la persona, no por lo que se pidió.
	granted := int64(2_500_000)
	var mandate struct {
		MandateID       string `json:"mandate_id"`
		MaxPerOperation *int64 `json:"max_per_operation"`
	}
	if err := d.holder.json("POST", "/v1/consent-requests/"+handoff+"/approve", map[string]any{
		"account_id":        holder.AccountID,
		"max_per_operation": granted,
		"max_total":         prompt.MaxTotal,
		"expires_in_days":   30,
	}, &mandate); err != nil {
		return err
	}

	say("concede %s por pago, la mitad de lo pedido", micros(granted, d.currency))
	return nil
}

// pollConsent descubre el permiso consultando, no esperando un webhook.
func (d *demo) pollConsent(requestID string) (*consentRequest, error) {
	d.step("La plataforma consulta si ya se concedió")

	deadline := time.Now().Add(30 * time.Second)
	for attempt := 1; ; attempt++ {
		var request consentRequest
		if err := d.partner.json("GET", "/v1/consent-requests/"+requestID, nil, &request); err != nil {
			return nil, err
		}

		switch request.Status {
		case "approved":
			say("aprobada en el intento %d · mandato %s", attempt, request.MandateID)
			say("y AQUÍ aprende la cuenta del titular: %s", request.AccountID)
			return &request, nil
		case "rejected", "expired":
			return nil, fmt.Errorf("la solicitud quedó en %q", request.Status)
		}

		if time.Now().After(deadline) {
			return nil, errors.New("el titular no respondió a tiempo")
		}
		time.Sleep(time.Second)
	}
}

type authorization struct {
	AuthorizationID string     `json:"authorization_id"`
	Status          string     `json:"status"`
	ExpiresAt       *time.Time `json:"expires_at"`
}

func (d *demo) authorize(payer, payee, mandateID string) (*authorization, error) {
	d.step("Autorizar — el dinero se retiene, todavía no se mueve")

	body := map[string]any{
		"payer_account_id": payer,
		"payee_account_id": payee,
		"amount":           d.amount,
		"currency":         d.currency,
	}
	if mandateID != "" {
		// El único campo que distingue los dos modelos.
		body["mandate_id"] = mandateID
	}

	var auth authorization
	// La clave de idempotencia lleva la hora para que repetir la demo autorice de
	// nuevo. En un cliente real la genera el carrito, y un reintento CONSERVA la
	// misma: es lo que impide cobrar dos veces tras un timeout.
	key := fmt.Sprintf("demo-%d", time.Now().UnixNano())
	if err := d.partner.jsonWithKey("POST", "/v1/authorizations", key, body, &auth); err != nil {
		return nil, err
	}

	// El importe se toma de lo pedido: la respuesta de `authorize` solo trae
	// identificador, estado y vencimiento. Lo demás se consulta con el GET.
	say("%s · %s · %s", auth.AuthorizationID, auth.Status, micros(d.amount, d.currency))
	if auth.ExpiresAt != nil {
		say("si nadie captura, se libera sola a las %s", auth.ExpiresAt.Format(time.Kitchen))
	}
	return &auth, nil
}

func (d *demo) settle(auth *authorization) error {
	d.step("Capturar — ahora sí se mueve")

	var captured authorization
	if err := d.partner.json("POST",
		"/v1/authorizations/"+auth.AuthorizationID+"/capture", nil, &captured); err != nil {
		return err
	}
	say("%s", captured.Status)

	d.step("Devolver una parte")

	var refund struct {
		Status        string `json:"status"`
		Refunded      int64  `json:"refunded"`
		RefundedTotal int64  `json:"refunded_total"`
		Refundable    int64  `json:"refundable"`
	}
	if err := d.partner.json("POST",
		"/v1/authorizations/"+auth.AuthorizationID+"/refund",
		map[string]any{"amount": d.amount / 2}, &refund); err != nil {
		return err
	}
	say("%s · devuelto %s · queda por devolver %s", refund.Status,
		micros(refund.RefundedTotal, d.currency), micros(refund.Refundable, d.currency))
	return nil
}

func (d *demo) showMandate(mandateID string) error {
	d.step("Qué queda del permiso")

	var mandate struct {
		Status    string `json:"status"`
		Consumed  int64  `json:"consumed"`
		Remaining *int64 `json:"remaining"`
	}
	if err := d.partner.json("GET", "/v1/mandates/"+mandateID, nil, &mandate); err != nil {
		return err
	}

	say("%s · consumido %s", mandate.Status, micros(mandate.Consumed, d.currency))
	if mandate.Remaining != nil {
		say("queda %s", micros(*mandate.Remaining, d.currency))
	}
	// Lo consumido baja al reembolsar porque se DERIVA de las autorizaciones; no
	// es un contador que alguien tenga que acordarse de bajar.
	say("nótese que el reembolso ya está descontado")
	return nil
}

// --------------------------------------------------------------- cliente HTTP

type api struct {
	base  string
	token string
	http  http.Client
}

func (a *api) json(method, path string, body, out any) error {
	return a.jsonWithKey(method, path, "", body, out)
}

func (a *api) jsonWithKey(method, path, idempotencyKey string, body, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequest(method, a.base+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	return a.do(req, out)
}

func (a *api) form(method, path string, values url.Values, out any) error {
	req, err := http.NewRequest(method, a.base+path, strings.NewReader(values.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return a.do(req, out)
}

func (a *api) do(req *http.Request, out any) error {
	if a.token != "" {
		req.Header.Set("Authorization", "Bearer "+a.token)
	}

	resp, err := a.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", req.Method, req.URL.Path, err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}

	if resp.StatusCode >= 300 {
		// El cuerpo del error se muestra tal cual: la tabla de errores del
		// contrato es lo que quien integra tiene que aprender a leer.
		return fmt.Errorf("%s %s → %d %s", req.Method, req.URL.Path,
			resp.StatusCode, strings.TrimSpace(string(payload)))
	}
	if out == nil || len(payload) == 0 {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("%s %s: respuesta ilegible: %w", req.Method, req.URL.Path, err)
	}
	return nil
}

// ----------------------------------------------------------------- presentación

func (d *demo) step(format string, args ...any) {
	d.steps++
	fmt.Printf("\n▸ %d · "+format+"\n", append([]any{d.steps}, args...)...)
}

func title(text string) {
	fmt.Printf("\n%s\n", text)
}

func say(format string, args ...any) {
	fmt.Printf("  "+format+"\n", args...)
}

// micros presenta un importe sin perder la escala: seis decimales existen y el
// caso que justifica todo esto —$0,001 por llamada— vive en el cuarto.
func micros(amount int64, currency string) string {
	units := amount / 1_000_000
	rest := amount % 1_000_000
	if rest < 0 {
		rest = -rest
	}
	text := fmt.Sprintf("%d.%06d", units, rest)
	text = strings.TrimRight(text, "0")
	if strings.HasSuffix(text, ".") {
		text += "00"
	}
	return text + " " + currency
}

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
