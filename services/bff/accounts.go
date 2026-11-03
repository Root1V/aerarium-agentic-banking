package bff

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/aibank/aibank/clients/go/coreclient"
	"github.com/google/uuid"
)

// Las cuentas básicas (nivel 1) y abrir una en la otra moneda (F3-09).

// BasicProductPrefix: el producto de nivel 1 de cada moneda es BASICA-<moneda>.
const BasicProductPrefix = "BASICA-"

// BasicCaps son los topes de la cuenta básica, en unidades de cada moneda.
type BasicCaps struct {
	MaxBalance, MaxTransaction int64
}

// basicCaps: en soles, los de la norma de cuentas básicas de la SBS para 2026
// (Resolución SBS 00704-2026, sobre la UIT de S/ 5.500): saldo de 1,9 UIT y 0,6
// UIT por operación. Se actualizan cada año con la UIT. El tope mensual de 2,8
// UIT (S/ 15.400) todavía no se hace cumplir. En dólares, PROVISIONALES: hay que
// validarlos con el asesor regulatorio.
var basicCaps = map[string]BasicCaps{
	"PEN": {MaxBalance: 10_450, MaxTransaction: 3_300},
	"USD": {MaxBalance: 1_000, MaxTransaction: 300},
}

// EnsureBasicProduct crea el producto de nivel 1 de una moneda si falta, y
// devuelve su código.
func EnsureBasicProduct(ctx context.Context, core *coreclient.Client, currency string) (string, error) {
	caps, ok := basicCaps[currency]
	if !ok {
		return "", errors.New("moneda no disponible: " + currency)
	}
	code := BasicProductPrefix + currency
	maxBalance, maxTx := caps.MaxBalance*1_000_000, caps.MaxTransaction*1_000_000
	_, err := core.CreateProduct(ctx, coreclient.NewProduct{
		Code: code, Name: "Cuenta básica " + currency, Currency: currency,
		MaxBalanceMicros: &maxBalance, MaxTransactionMicros: &maxTx,
	})
	if err != nil && !errors.Is(err, coreclient.ErrAlreadyExists) {
		return "", err
	}
	return code, nil
}

// handleOpenAccount abre una cuenta básica en otra moneda para el titular: una
// por moneda, a su nombre.
func (s *Server) handleOpenAccount(w http.ResponseWriter, r *http.Request, p *Principal) {
	var body struct {
		Currency string `json:"currency"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	currency := strings.ToUpper(strings.TrimSpace(body.Currency))
	if _, ok := basicCaps[currency]; !ok {
		writeError(w, http.StatusBadRequest, "invalid_request", "solo hay cuentas en soles y en dólares")
		return
	}
	ctx := r.Context()
	accounts, err := s.core.ListCustomerAccounts(ctx, p.CustomerID)
	if err != nil {
		s.writeCoreError(w, r, err)
		return
	}
	holder := ""
	for _, a := range accounts {
		if a.GetParentAccountId() != "" {
			continue
		}
		if a.Currency == currency {
			writeError(w, http.StatusConflict, "account_exists", "ya tienes una cuenta en esa moneda")
			return
		}
		if holder == "" {
			holder = a.Name
		}
	}
	if holder == "" {
		// Sin una primera cuenta no hay a nombre de quién abrir la segunda: la
		// primera nace del alta, con su verificación de identidad.
		writeError(w, http.StatusConflict, "no_account", "primero abre tu cuenta")
		return
	}
	product, err := EnsureBasicProduct(ctx, s.core, currency)
	if err != nil {
		s.writeCoreError(w, r, err)
		return
	}
	account, err := s.core.OpenCustomerAccount(ctx, "ACC-"+uuid.NewString(), holder, p.CustomerID, product)
	if err != nil {
		s.writeCoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, accountJSON{ID: account.Id, Name: account.Name, Currency: account.Currency})
}
