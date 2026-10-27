package bff

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/aibank/aibank/clients/go/coreclient"
	corev1 "github.com/aibank/aibank/clients/go/corev1"
	"github.com/aibank/aibank/services/aliases"
	"github.com/google/uuid"
)

// Saldo de prueba, para recorrer la app con dinero sin pasar por un depósito
// real. SOLO existe en el sandbox: el binario lo monta únicamente con la
// autenticación de desarrollo, y fuera de ella esta ruta no existe.
//
// Además del saldo da un destinatario de prueba: sin alguien a quien enviarle,
// la confirmación de operaciones no se puede probar desde el teléfono. Es UNO
// por cuenta, siempre el mismo y con el mismo celular: si cada saldo creara
// otro, los recientes se llenarían de Juanes repetidos.

// SandboxFundsMicros es lo que se acredita cada vez: 250, bajo el tope por
// operación de la cuenta de nivel 1 (300). Se puede pedir varias veces, hasta su
// tope de saldo.
const SandboxFundsMicros int64 = 250_000_000

type sandboxConfig struct {
	cashPrefix    string
	productPrefix string
}

// WithSandboxFunds habilita el saldo de prueba. `cashPrefix` + moneda es la caja
// que lo financia; `productPrefix` + moneda, el producto del destinatario.
func (s *Server) WithSandboxFunds(cashPrefix, productPrefix string) *Server {
	s.sandbox = &sandboxConfig{cashPrefix: cashPrefix, productPrefix: productPrefix}
	return s
}

func (s *Server) sandboxRoutes(mux *http.ServeMux) {
	// La app pregunta si existe para decidir si muestra el botón.
	mux.HandleFunc("GET /v1/sandbox", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"available": true, "amount_micros": SandboxFundsMicros})
	})
	mux.Handle("POST /v1/sandbox/funds", s.requireAuth(s.handleSandboxFunds))
}

type sandboxFundsResponse struct {
	AmountMicros int64       `json:"amount_micros"`
	Currency     string      `json:"currency"`
	Payee        accountJSON `json:"payee"`
	// PayeePhone es el celular de prueba del destinatario, para probar el envío
	// a un número. Vacío si el directorio de celulares no está habilitado.
	PayeePhone string `json:"payee_phone,omitempty"`
}

func (s *Server) handleSandboxFunds(w http.ResponseWriter, r *http.Request, p *Principal) {
	var body struct {
		AccountID string `json:"account_id"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	ctx := r.Context()
	account, err := s.authorizeAccount(ctx, p, body.AccountID)
	if err != nil {
		s.writeCoreError(w, r, err)
		return
	}
	currency := account.Currency

	cash, err := s.core.GetAccount(ctx, s.sandbox.cashPrefix+currency)
	if err != nil {
		s.writeCoreError(w, r, err)
		return
	}
	if _, err := s.core.Post(ctx, "sandbox-funds-"+uuid.NewString(), "sandbox_funding", []coreclient.Entry{
		coreclient.Debit(cash.Id, SandboxFundsMicros, currency),
		coreclient.Credit(account.Id, SandboxFundsMicros, currency),
	}, "saldo de prueba"); err != nil {
		s.writeCoreError(w, r, err)
		return
	}

	payee, err := s.testPayee(ctx, account.Id, currency)
	if err != nil {
		s.writeCoreError(w, r, err)
		return
	}

	resp := sandboxFundsResponse{
		AmountMicros: SandboxFundsMicros,
		Currency:     currency,
		Payee:        accountJSON{ID: payee.Id, Name: payee.Name, Currency: payee.Currency},
	}
	if s.aliases != nil {
		phone, err := s.testPayeePhone(ctx, payee)
		if err != nil {
			s.aliasError(w, r, err)
			return
		}
		resp.PayeePhone = phone
	}
	writeJSON(w, http.StatusOK, resp)
}

// testPayee devuelve el destinatario de prueba de la cuenta y lo crea la primera
// vez. Su código sale de la cuenta, así que pedir saldo otra vez lo encuentra.
func (s *Server) testPayee(ctx context.Context, accountID, currency string) (*corev1.Account, error) {
	code := "PAYEE-" + strings.ToUpper(strings.ReplaceAll(accountID, "-", ""))
	payee, err := s.core.GetAccount(ctx, code)
	if err == nil {
		return payee, nil
	}
	if !errors.Is(err, coreclient.ErrNotFound) {
		return nil, err
	}
	product := s.sandbox.productPrefix + currency
	if _, err := s.core.CreateProduct(ctx, coreclient.NewProduct{
		Code: product, Name: "Cuenta personal " + currency, Currency: currency,
	}); err != nil && !errors.Is(err, coreclient.ErrAlreadyExists) {
		return nil, err
	}
	payee, err = s.core.OpenCustomerAccount(ctx, code, "Juan Pérez (prueba)", uuid.NewString(), product)
	if errors.Is(err, coreclient.ErrAlreadyExists) {
		// Dos pedidos de saldo a la vez: el otro lo creó primero.
		return s.core.GetAccount(ctx, code)
	}
	return payee, err
}

// testPayeePhone devuelve el celular del destinatario de prueba, y le da uno la
// primera vez.
func (s *Server) testPayeePhone(ctx context.Context, payee *corev1.Account) (string, error) {
	mine, err := s.aliases.svc.Mine(ctx, payee.OwnerId)
	if err == nil {
		return mine.Phone, nil
	}
	if !errors.Is(err, aliases.ErrNotFound) {
		return "", err
	}
	return s.aliases.svc.RegisterVerified(ctx, payee.OwnerId, payee.Id, sandboxPhone())
}
