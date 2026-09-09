// Alta de una integración de socio y de las cuentas internas que necesita.
//
// Deja el entorno listo para que un socio pueda integrar: crea el producto de
// cuentas de agente, la cuenta de retención de la moneda, la caja del sandbox y
// las credenciales OAuth2.
//
// El secreto se imprime UNA sola vez. La base guarda su hash; no hay forma de
// recuperarlo después, solo de rotarlo.
//
//	go run ./services/mercatus/cmd/bootstrap \
//	  -client-id mercatus_sandbox -name "Mercatus" -currencies USD
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	corev1 "github.com/aibank/aibank/clients/go/corev1"
	"github.com/aibank/aibank/clients/go/coreclient"
	"github.com/aibank/aibank/services/oauth"
	_ "github.com/lib/pq"
)

func main() {
	var (
		clientID   = flag.String("client-id", "", "identificador de la integración (obligatorio)")
		name       = flag.String("name", "", "nombre de la integración")
		currencies = flag.String("currencies", "USD", "monedas a habilitar, separadas por coma")
		sandbox    = flag.Bool("sandbox", true, "crea también la caja que financia saldos de prueba")
		rotate     = flag.Bool("rotate", false, "rota el secreto de una integración ya existente")
	)
	flag.Parse()

	if *clientID == "" {
		fmt.Fprintln(os.Stderr, "falta -client-id")
		flag.Usage()
		os.Exit(2)
	}

	if err := run(*clientID, *name, *currencies, *sandbox, *rotate); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(clientID, name, currencies string, sandbox, rotate bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	db, err := sql.Open("postgres", env("DATABASE_URL",
		"postgres://aibank:aibank_dev@localhost:5434/aibank?sslmode=disable"))
	if err != nil {
		return err
	}
	defer db.Close()

	store := oauth.NewStore(db)
	if err := store.Migrate(ctx); err != nil {
		return fmt.Errorf("migrar esquema oauth: %w", err)
	}

	if rotate {
		secret, err := store.RotateSecret(ctx, clientID)
		if err != nil {
			return err
		}
		fmt.Printf("client_id:     %s\n", clientID)
		fmt.Printf("client_secret: %s\n", secret)
		fmt.Printf("\nEl secreto anterior sigue sirviendo %s más.\n", oauth.SecretGraceWindow)
		return nil
	}

	core, err := coreclient.Dial(ctx, env("CORE_ADDR", "127.0.0.1:50051"))
	if err != nil {
		return err
	}
	defer core.Close()

	var products []string
	for _, currency := range strings.Split(currencies, ",") {
		currency = strings.ToUpper(strings.TrimSpace(currency))
		if len(currency) != 3 {
			return fmt.Errorf("moneda inválida: %q", currency)
		}

		product := "AGENT-" + currency
		if err := ensureProduct(ctx, core, product, currency); err != nil {
			return err
		}
		products = append(products, currency+"="+product)

		// Sin la cuenta de retención el core no puede autorizar: el dinero de una
		// autorización viva tiene que ir a algún sitio que no sea el receptor.
		holds := "AUTH-HOLDS-" + currency
		if err := ensureAccount(ctx, core, holds, "Retenciones de autorizaciones",
			corev1.AccountType_ACCOUNT_TYPE_LIABILITY, currency); err != nil {
			return err
		}

		if sandbox {
			cash := "SANDBOX-CASH-" + currency
			if err := ensureAccount(ctx, core, cash, "Caja del sandbox",
				corev1.AccountType_ACCOUNT_TYPE_ASSET, currency); err != nil {
				return err
			}
			account, err := core.GetAccount(ctx, cash)
			if err != nil {
				return err
			}
			fmt.Printf("MERCATUS_SANDBOX_CASH=%s   # %s\n", account.Id, cash)
		}
	}

	secret, err := store.Register(ctx, clientID, nameOr(name, clientID), []string{
		oauth.ScopePaymentsRead, oauth.ScopePaymentsWrite, oauth.ScopeAccountsWrite,
	})
	if err != nil {
		return fmt.Errorf("registrar integración: %w", err)
	}

	fmt.Printf("MERCATUS_PRODUCTS=%s\n", strings.Join(products, ","))
	fmt.Println()
	fmt.Println("Credenciales de la integración — el secreto NO se puede recuperar después:")
	fmt.Printf("  client_id:     %s\n", clientID)
	fmt.Printf("  client_secret: %s\n", secret)
	fmt.Println()
	fmt.Println("Scopes: payments:read payments:write accounts:write")
	return nil
}

// ensureProduct crea el producto si falta. Repetir el alta es inofensivo.
func ensureProduct(ctx context.Context, core *coreclient.Client, code, currency string) error {
	_, err := core.CreateProduct(ctx, coreclient.NewProduct{
		Code:     code,
		Name:     "Cuenta de agente " + currency,
		Currency: currency,
	})
	if err != nil && !errors.Is(err, coreclient.ErrAlreadyExists) {
		return fmt.Errorf("crear producto %s: %w", code, err)
	}
	return nil
}

func ensureAccount(ctx context.Context, core *coreclient.Client, code, name string, accountType corev1.AccountType, currency string) error {
	_, err := core.CreateInternalAccount(ctx, code, name, accountType, currency)
	if err != nil && !errors.Is(err, coreclient.ErrAlreadyExists) {
		return fmt.Errorf("crear cuenta %s: %w", code, err)
	}
	return nil
}

func nameOr(name, fallback string) string {
	if name != "" {
		return name
	}
	return fallback
}

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
