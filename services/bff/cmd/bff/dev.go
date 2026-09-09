package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/aibank/aibank/clients/go/coreclient"
	"github.com/aibank/aibank/services/bff/sim"
	"github.com/google/uuid"
)

// El simulador de titulares del sandbox.
//
// El modelo B solo se puede probar si existe una persona con cuenta en AIBank
// que conceda el permiso. En producción esa persona llega por el onboarding
// —KYC incluido— y no hay atajo. En el sandbox tiene que haber uno, porque si no
// quien integra no puede ni empezar: pediría un consentimiento que nadie podría
// aprobar nunca.
//
// Todo lo de este archivo cuelga de ALLOW_DEV_AUTH y de un binario que se niega
// a arrancar sin él. No es una puerta trasera desactivada por configuración: es
// código que solo existe en el camino del sandbox.

const (
	// Producto de las cuentas de titular del sandbox. Distinto del de agentes
	// (AGENT-*) porque son cosas distintas: una cuenta de persona y la cuenta de
	// un agente de la plataforma no tienen por qué compartir topes.
	holderProductPrefix = "RETAIL-"
	// Caja que financia los saldos de prueba. La crea el bootstrap.
	sandboxCashPrefix = "SANDBOX-CASH-"
)

type devHolderRequest struct {
	Currency string `json:"currency"`
	Name     string `json:"name"`
	// InitialBalance en micras. Sin saldo, el titular puede conceder un permiso
	// pero ningún pago bajo él llegaría a capturarse.
	InitialBalance int64 `json:"initial_balance"`
}

type devHolderResponse struct {
	CustomerID string `json:"customer_id"`
	AccountID  string `json:"account_id"`
	// Token de sesión del titular, para las llamadas del canal.
	Token    string `json:"token"`
	Currency string `json:"currency"`
	Balance  int64  `json:"balance"`
}

// devGate protege las rutas del simulador con una clave compartida.
//
// En un sandbox en el portátil sobra: nadie más llega. En uno ALOJADO no, y la
// diferencia no es teórica — `POST /dev/sessions` emite una sesión para el
// customer_id que se le pida, así que sin puerta cualquiera en internet puede
// hablar por cualquier titular de prueba, incluidos los de otro socio.
//
// La clave se compara en tiempo constante y no aparece en el mensaje de error:
// un 401 que distingue "clave incorrecta" de "falta la clave" ya es una pista.
func devGate(next http.HandlerFunc, key string, log *slog.Logger) http.HandlerFunc {
	if key == "" {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		supplied := r.Header.Get("X-Dev-Key")
		if subtle.ConstantTimeCompare([]byte(supplied), []byte(key)) != 1 {
			log.Warn("acceso rechazado al simulador de titulares", "ruta", r.URL.Path)
			devError(w, http.StatusUnauthorized, "falta o no coincide X-Dev-Key")
			return
		}
		next(w, r)
	}
}

func mountDevRoutes(mux *http.ServeMux, core *coreclient.Client, auth *sim.Authenticator, log *slog.Logger) {
	key := os.Getenv("DEV_API_KEY")

	mux.HandleFunc("POST /dev/holders", devGate(func(w http.ResponseWriter, r *http.Request) {
		var req devHolderRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && r.ContentLength > 0 {
			devError(w, http.StatusBadRequest, "el cuerpo no es JSON válido")
			return
		}

		currency := strings.ToUpper(strings.TrimSpace(req.Currency))
		if currency == "" {
			currency = "USD"
		}
		if len(currency) != 3 {
			devError(w, http.StatusBadRequest, "moneda inválida")
			return
		}
		if req.InitialBalance < 0 {
			devError(w, http.StatusBadRequest, "el saldo inicial no puede ser negativo")
			return
		}

		ctx := r.Context()
		product := holderProductPrefix + currency
		if _, err := core.CreateProduct(ctx, coreclient.NewProduct{
			Code:     product,
			Name:     "Cuenta personal " + currency,
			Currency: currency,
		}); err != nil && !errors.Is(err, coreclient.ErrAlreadyExists) {
			devFailed(w, log, "crear producto de titular", err)
			return
		}

		customerID := uuid.NewString()
		name := req.Name
		if name == "" {
			name = "Titular de prueba"
		}

		account, err := core.OpenCustomerAccount(ctx,
			"HOLDER-"+strings.ToUpper(customerID[:8]), name, customerID, product)
		if err != nil {
			devFailed(w, log, "abrir cuenta de titular", err)
			return
		}

		if req.InitialBalance > 0 {
			cash, err := core.GetAccount(ctx, sandboxCashPrefix+currency)
			if err != nil {
				devFailed(w, log, "buscar la caja del sandbox", err)
				return
			}
			if _, err := core.Post(ctx, "dev-holder-fund-"+account.Id, "sandbox_funding",
				[]coreclient.Entry{
					coreclient.Debit(cash.Id, req.InitialBalance, currency),
					coreclient.Credit(account.Id, req.InitialBalance, currency),
				}, "saldo inicial del titular de prueba"); err != nil {
				devFailed(w, log, "fondear al titular", err)
				return
			}
		}

		// El dispositivo forma parte de la evidencia del consentimiento: el
		// mandato guarda cuál aprobó. Aquí es de mentira, pero tiene que existir
		// para que el rastro tenga la misma forma que en producción.
		token := auth.Issue(customerID, "dev-device-"+customerID[:8])

		writeDevJSON(w, http.StatusCreated, devHolderResponse{
			CustomerID: customerID,
			AccountID:  account.Id,
			Token:      token,
			Currency:   currency,
			Balance:    req.InitialBalance,
		})
	}, key, log))

	// Recuperar la sesión de un titular ya creado, para reanudar una prueba sin
	// tener que crear otra persona.
	mux.HandleFunc("POST /dev/sessions", devGate(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			CustomerID string `json:"customer_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.CustomerID == "" {
			devError(w, http.StatusBadRequest, "falta customer_id")
			return
		}
		writeDevJSON(w, http.StatusCreated, map[string]string{
			"customer_id": body.CustomerID,
			"token":       auth.Issue(body.CustomerID, "dev-device"),
		})
	}, key, log))

	log.Warn("rutas de sandbox montadas",
		"rutas", "POST /dev/holders, POST /dev/sessions",
		"protegidas", key != "")
	if key == "" {
		log.Warn("el simulador de titulares está ABIERTO: pon DEV_API_KEY si esto es accesible desde fuera")
	}
}

func devFailed(w http.ResponseWriter, log *slog.Logger, what string, err error) {
	log.Error("sandbox: "+what, "error", err)
	devError(w, http.StatusServiceUnavailable, "no se pudo "+what)
}

func devError(w http.ResponseWriter, status int, message string) {
	writeDevJSON(w, status, map[string]string{"error": message})
}

func writeDevJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
