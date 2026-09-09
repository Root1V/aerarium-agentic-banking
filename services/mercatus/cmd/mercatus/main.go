// Servidor de la API de socio (riel de pago para plataformas de agentes).
//
// Variables de entorno:
//
//	DATABASE_URL          PostgreSQL (credenciales y enlaces de sub-cuenta)
//	CORE_ADDR             core gRPC (por defecto 127.0.0.1:50051)
//	BIND_ADDR             dirección de escucha (por defecto 127.0.0.1:8081)
//	OAUTH_ISSUER          identificador del emisor de tokens
//	OAUTH_SIGNING_KEY     clave de firma en base64, 32 bytes o más
//	OAUTH_PREVIOUS_KEY    clave anterior, para rotar sin invalidar tokens en vuelo
//	MERCATUS_SANDBOX      "true" habilita el saldo inicial al abrir cuenta
//	MERCATUS_PRODUCTS     moneda=producto separados por coma: "USD=AGENT-USD,PEN=AGENT-PEN"
//	MERCATUS_SANDBOX_CASH cuenta de caja que financia los saldos de sandbox
package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/aibank/aibank/clients/go/coreclient"
	"github.com/aibank/aibank/services/mercatus"
	"github.com/aibank/aibank/services/oauth"
	_ "github.com/lib/pq"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	if err := run(log); err != nil {
		log.Error("el servicio terminó con error", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	signingKey, err := decodeKey("OAUTH_SIGNING_KEY")
	if err != nil {
		return err
	}
	previousKey, err := decodeKey("OAUTH_PREVIOUS_KEY")
	if err != nil {
		return err
	}

	db, err := sql.Open("postgres", env("DATABASE_URL", "postgres://aibank:aibank_dev@localhost:5434/aibank?sslmode=disable"))
	if err != nil {
		return err
	}
	defer db.Close()

	store := oauth.NewStore(db)
	if err := store.Migrate(ctx); err != nil {
		return err
	}

	issuer, err := oauth.NewIssuer(env("OAUTH_ISSUER", "https://api.aibank.local"), signingKey, previousKey)
	if err != nil {
		return err
	}

	core, err := coreclient.Dial(ctx, env("CORE_ADDR", "127.0.0.1:50051"))
	if err != nil {
		return err
	}
	defer core.Close()

	sandbox := os.Getenv("MERCATUS_SANDBOX") == "true"
	cashAccount, err := sandboxCashAccount(ctx, core, sandbox)
	if err != nil {
		return err
	}

	server := mercatus.NewServer(core, oauth.NewServer(store, issuer, log), store, mercatus.Config{
		Sandbox:              sandbox,
		ProductByCurrency:    productsFromEnv(),
		SandboxCashAccountID: cashAccount,
	}, log)

	addr := env("BIND_ADDR", "127.0.0.1:8081")
	httpServer := &http.Server{
		Addr:    addr,
		Handler: server.Handler(),
		// Un cliente lento no puede quedarse con una conexión indefinidamente.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()

	log.Info("API de socio escuchando", "addr", addr,
		"sandbox", os.Getenv("MERCATUS_SANDBOX") == "true")

	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// decodeKey lee una clave en base64 de una variable de entorno.
//
// No hay valor por defecto para la clave de firma a propósito: un servicio que
// arranca con una clave conocida es peor que uno que no arranca.
func decodeKey(name string) ([]byte, error) {
	raw := os.Getenv(name)
	if raw == "" {
		if name == "OAUTH_SIGNING_KEY" {
			return nil, errors.New("falta OAUTH_SIGNING_KEY (base64 de 32 bytes o más)")
		}
		return nil, nil
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, errors.New(name + " no es base64 válido")
	}
	return key, nil
}

// sandboxCashAccount resuelve la caja que financia los saldos de prueba.
//
// El identificador se puede fijar con MERCATUS_SANDBOX_CASH, pero lo normal es
// no tenerlo: lo genera el alta de la integración, y obligar a copiarlo a mano
// convierte levantar el entorno en dos pasos encadenados. Por eso, si falta, se
// busca por código —el que crea el bootstrap— y se falla en el arranque si no
// está: un sandbox que acepta abrir cuentas y luego no puede acreditarles saldo
// es peor que uno que no arranca.
func sandboxCashAccount(ctx context.Context, core *coreclient.Client, sandbox bool) (string, error) {
	if id := os.Getenv("MERCATUS_SANDBOX_CASH"); id != "" {
		return id, nil
	}
	if !sandbox {
		return "", nil
	}

	code := env("MERCATUS_SANDBOX_CASH_CODE", "SANDBOX-CASH-USD")
	account, err := core.GetAccount(ctx, code)
	if err != nil {
		return "", fmt.Errorf("buscar la caja del sandbox %q: %w "+
			"(¿corriste el alta de la integración?)", code, err)
	}
	return account.Id, nil
}

// productsFromEnv lee el mapa de moneda a producto de MERCATUS_PRODUCTS, con el
// formato "USD=AGENT-USD,PEN=AGENT-PEN".
//
// Una moneda que no esté en el mapa no se puede abrir. Es lo correcto: cada
// producto define la moneda y los topes de la cuenta, y abrir una cuenta bajo el
// producto de otra moneda mezclaría dos monedas en un mismo saldo.
func productsFromEnv() map[string]string {
	raw := env("MERCATUS_PRODUCTS", "USD=AGENT-USD")
	products := make(map[string]string)
	for _, pair := range strings.Split(raw, ",") {
		currency, code, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if ok && currency != "" && code != "" {
			products[strings.ToUpper(currency)] = code
		}
	}
	return products
}

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
