package bff

import (
	"context"
	"errors"
	"html/template"
	"net/http"
	"strconv"
	"time"

	"github.com/aibank/aibank/clients/go/coreclient"
	corev1 "github.com/aibank/aibank/clients/go/corev1"
	"github.com/aibank/aibank/services/aliases"
	"github.com/aibank/aibank/services/ratelimit"
	"github.com/aibank/aibank/services/receipts"
)

// Constancias verificables. Contra las capturas falsas de un pago: la constancia
// lleva un código, y quien la recibe —sea o no cliente— le pregunta al banco si
// el pago es real. La verificación es pública y con límite de intentos; muestra
// lo que la constancia dice y nada más, con los nombres acortados.

type receiptConfig struct {
	store   *receipts.Store
	limiter *ratelimit.Limiter
}

// WithReceipts habilita emitir y verificar constancias.
func (s *Server) WithReceipts(store *receipts.Store) *Server {
	s.receipts = &receiptConfig{store: store, limiter: ratelimit.NewLimiter(defaultLookupRate, defaultLookupBurst)}
	return s
}

func (s *Server) receiptRoutes(mux *http.ServeMux) {
	mux.Handle("POST /v1/accounts/{accountID}/movements/{transactionID}/receipt", s.requireAuth(s.handleIssueReceipt))
	mux.HandleFunc("GET /v1/receipts/{code}", s.limitedReceipt(s.handleVerifyReceipt))
	mux.HandleFunc("GET /c/{code}", s.limitedReceipt(s.handleReceiptPage))
}

// receiptKinds son los movimientos que tienen constancia: lo que se le paga o se
// le cobra a alguien. Apartar en un bolsillo o un depósito de prueba, no.
var receiptKinds = map[string]bool{"p2p_transfer": true, "authorization_hold": true}

type receiptJSON struct {
	Code     string    `json:"code"`
	Kind     string    `json:"kind"`
	Amount   moneyJSON `json:"amount"`
	From     string    `json:"from"`
	To       string    `json:"to"`
	PostedAt time.Time `json:"posted_at"`
	// URL: dónde verificarla, para quien no tiene la app.
	URL string `json:"url"`
	// Status, al verificar: `completed`, o en qué quedó el pago de una plataforma.
	Status string `json:"status,omitempty"`
}

func (s *Server) handleIssueReceipt(w http.ResponseWriter, r *http.Request, p *Principal) {
	ctx := r.Context()
	accountID, txID := r.PathValue("accountID"), r.PathValue("transactionID")
	account, err := s.authorizeAccount(ctx, p, accountID)
	if err != nil {
		s.writeCoreError(w, r, err)
		return
	}
	m, err := s.findMovement(ctx, accountID, txID)
	if err != nil {
		s.writeCoreError(w, r, err)
		return
	}
	if m == nil {
		writeError(w, http.StatusNotFound, "not_found", "ese movimiento no es de esta cuenta")
		return
	}
	if !receiptKinds[m.Kind] {
		writeError(w, http.StatusUnprocessableEntity, "not_receiptable", "este movimiento no lleva constancia")
		return
	}
	// Los nombres salen del banco, no de lo que diga la app.
	self := aliases.MaskName(account.Name)
	other := aliases.MaskName(m.CounterpartyName)
	if m.Kind == "authorization_hold" {
		if holds, err := s.core.LookupHolds(ctx, []string{txID}); err == nil {
			if hold, ok := holds[txID]; ok {
				other = s.merchantName(ctx, hold.Grantee)
			}
		}
	}
	from, to := self, other
	if m.Direction == corev1.Direction_DIRECTION_CREDIT {
		from, to = other, self
	}
	receipt, err := s.receipts.store.Issue(ctx, receipts.Receipt{
		TransactionID: txID, AccountID: accountID, CustomerID: p.CustomerID, Kind: m.Kind,
		AmountMicros: m.AmountMicros, Currency: m.Currency, FromName: from, ToName: to, PostedAt: m.PostedAt,
	})
	if err != nil {
		s.logger.ErrorContext(ctx, "emitir constancia", "error", err)
		writeError(w, http.StatusServiceUnavailable, "unavailable", "no pudimos emitir la constancia")
		return
	}
	writeJSON(w, http.StatusCreated, s.toReceiptJSON(r, receipt))
}

// findMovement busca un movimiento en lo reciente de la cuenta.
func (s *Server) findMovement(ctx context.Context, accountID, txID string) (*coreclient.Movement, error) {
	cursor := ""
	for page := 0; page < 10; page++ {
		statement, err := s.core.ListMovements(ctx, accountID, 100, cursor)
		if err != nil {
			return nil, err
		}
		for i := range statement.Movements {
			if statement.Movements[i].TransactionID == txID {
				return &statement.Movements[i], nil
			}
		}
		if statement.NextCursor == "" {
			return nil, nil
		}
		cursor = statement.NextCursor
	}
	return nil, nil
}

func (s *Server) toReceiptJSON(r *http.Request, rc *receipts.Receipt) receiptJSON {
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return receiptJSON{Code: rc.Code, Kind: rc.Kind,
		Amount: moneyJSON{AmountMicros: rc.AmountMicros, Currency: rc.Currency},
		From:   rc.FromName, To: rc.ToName, PostedAt: rc.PostedAt,
		URL: scheme + "://" + r.Host + "/c/" + rc.Code}
}

func (s *Server) limitedReceipt(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Con límite: un código no se adivina, pero tampoco se deja probar mil.
		if ok, wait := s.receipts.limiter.Allow(clientIP(r)); !ok {
			w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
			writeError(w, http.StatusTooManyRequests, "rate_limited", "demasiados intentos; espera un momento")
			return
		}
		h(w, r)
	}
}

// verify devuelve la constancia con el estado de hoy, o nil si no existe.
func (s *Server) verify(r *http.Request) (*receiptJSON, error) {
	rc, err := s.receipts.store.Verify(r.Context(), r.PathValue("code"))
	if errors.Is(err, receipts.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := s.toReceiptJSON(r, rc)
	out.Status = "completed"
	if rc.Kind == "authorization_hold" {
		// Un pago a una plataforma pudo devolverse después: se dice en qué quedó.
		if holds, err := s.core.LookupHolds(r.Context(), []string{rc.TransactionID}); err == nil {
			if hold, ok := holds[rc.TransactionID]; ok {
				if auth, err := s.core.GetAuthorization(r.Context(), hold.AuthorizationID); err == nil {
					out.Status = paymentStatusName(auth)
				}
			}
		}
	}
	return &out, nil
}

func (s *Server) handleVerifyReceipt(w http.ResponseWriter, r *http.Request) {
	out, err := s.verify(r)
	if err != nil {
		s.logger.ErrorContext(r.Context(), "verificar constancia", "error", err)
		writeError(w, http.StatusServiceUnavailable, "unavailable", "no pudimos verificar la constancia")
		return
	}
	if out == nil {
		writeError(w, http.StatusNotFound, "receipt_not_found",
			"no existe una constancia con ese código: el comprobante que te mostraron no es de Aerarium")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

var receiptPage = template.Must(template.New("c").Parse(`<!doctype html>
<html lang="es"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Verificar constancia · Aerarium</title>
<style>body{font:16px/1.5 -apple-system,system-ui,sans-serif;max-width:28rem;margin:2rem auto;padding:0 1rem;color:#222}
.ok{border-left:4px solid #2e7d32;padding:.5rem 1rem}.no{border-left:4px solid #c62828;padding:.5rem 1rem}
dt{color:#666;font-size:.9rem}dd{margin:0 0 .75rem;font-weight:600}</style></head><body>
<h1>Verificar constancia</h1>
{{if .}}<div class="ok"><p><strong>Este pago es real.</strong> Lo registró Aerarium.</p>
<dl><dt>Monto</dt><dd>{{.Amount}}</dd><dt>De</dt><dd>{{.From}}</dd><dt>Para</dt><dd>{{.To}}</dd>
<dt>Fecha</dt><dd>{{.When}}</dd><dt>Estado</dt><dd>{{.Status}}</dd><dt>Código</dt><dd>{{.Code}}</dd></dl>
<p>Compara estos datos con lo que te mostraron: si no coinciden, el comprobante no es este pago.</p></div>
{{else}}<div class="no"><p><strong>No existe una constancia con ese código.</strong></p>
<p>El comprobante que te mostraron no es de Aerarium. No entregues nada hasta ver el dinero en tu cuenta.</p></div>{{end}}
</body></html>`))

type receiptView struct{ Amount, From, To, When, Status, Code string }

var statusWords = map[string]string{
	"completed": "Completado", "held": "Retenido, todavía no cobrado", "captured": "Cobrado",
	"partially_refunded": "Devuelto en parte", "refunded": "Devuelto", "released": "No se cobró",
	"expired": "No se cobró",
}

func (s *Server) handleReceiptPage(w http.ResponseWriter, r *http.Request) {
	out, err := s.verify(r)
	if err != nil {
		http.Error(w, "no pudimos verificar la constancia; intenta de nuevo", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if out == nil {
		w.WriteHeader(http.StatusNotFound)
		_ = receiptPage.Execute(w, nil)
		return
	}
	lima, _ := time.LoadLocation(s.timeZone)
	_ = receiptPage.Execute(w, receiptView{
		Amount: formatMicros(out.Amount.AmountMicros) + " " + out.Amount.Currency,
		From:   out.From, To: out.To,
		When:   out.PostedAt.In(lima).Format("02/01/2006 15:04"),
		Status: statusWords[out.Status], Code: out.Code,
	})
}

// formatMicros: 50000000 → "50,00" y 1000 → "0,001": dos decimales, o los que
// hagan falta para no mostrar un micropago como cero. Como la app.
func formatMicros(micros int64) string {
	sign := ""
	if micros < 0 {
		sign, micros = "-", -micros
	}
	frac := strconv.FormatInt(micros%1_000_000+1_000_000, 10)[1:]
	for len(frac) > 2 && frac[len(frac)-1] == '0' {
		frac = frac[:len(frac)-1]
	}
	return sign + strconv.FormatInt(micros/1_000_000, 10) + "," + frac
}
