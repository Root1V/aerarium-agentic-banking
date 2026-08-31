// Package sim es un riel de pagos simulado.
//
// Existe para construir y probar el sistema completo sin ningún contrato firmado
// con un proveedor, y se queda para siempre como herramienta de pruebas: reproduce
// las patologías que un riel real tiene a diario y que rara vez se pueden provocar
// contra el sandbox de un tercero — timeouts, rechazos, notificaciones duplicadas
// y entregas fuera de orden.
//
// El comportamiento es DETERMINISTA: se programa explícitamente (FailNext,
// RejectNext) en vez de usar azar, para que un test que falla se pueda reproducir.
package sim

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/aibank/aibank/adapters/rails"
	"github.com/google/uuid"
)

// Rail es un riel simulado en memoria.
type Rail struct {
	mu sync.Mutex

	name    string
	aliases map[string]rails.AliasTarget
	// sent recuerda los envíos por TransferID: reenviar la misma orden devuelve el
	// mismo recibo, igual que hace un riel real bien implementado.
	sent map[string]*rails.SendReceipt

	latency     time.Duration
	failNext    int
	rejectNext  int
	sentCallLog []rails.SendRequest
}

func New(name string) *Rail {
	return &Rail{
		name:    name,
		aliases: make(map[string]rails.AliasTarget),
		sent:    make(map[string]*rails.SendReceipt),
	}
}

func (r *Rail) Name() string { return r.name }

// RegisterAlias da de alta un destinatario alcanzable por el riel.
func (r *Rail) RegisterAlias(alias, holder, institution string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.aliases[alias] = rails.AliasTarget{Alias: alias, HolderName: holder, Institution: institution}
}

// SetLatency simula la demora del riel.
func (r *Rail) SetLatency(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.latency = d
}

// FailNext hace que los próximos n envíos respondan ErrUnavailable, como un
// timeout de red: el llamador NO sabe si la operación se procesó o no.
func (r *Rail) FailNext(n int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failNext = n
}

// RejectNext hace que los próximos n envíos sean rechazados por el riel.
func (r *Rail) RejectNext(n int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rejectNext = n
}

// SendCount informa cuántas órdenes llegaron efectivamente al riel.
func (r *Rail) SendCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sentCallLog)
}

func (r *Rail) ResolveAlias(ctx context.Context, alias string) (*rails.AliasTarget, error) {
	if err := r.wait(ctx); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	target, ok := r.aliases[alias]
	if !ok {
		return nil, fmt.Errorf("%w: %s", rails.ErrUnknownAlias, alias)
	}
	return &target, nil
}

func (r *Rail) Send(ctx context.Context, req rails.SendRequest) (*rails.SendReceipt, error) {
	if err := r.wait(ctx); err != nil {
		return nil, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// Idempotencia del propio riel: la misma orden devuelve el mismo recibo.
	if receipt, ok := r.sent[req.TransferID]; ok {
		return receipt, nil
	}

	if _, ok := r.aliases[req.Alias]; !ok {
		return nil, fmt.Errorf("%w: %s", rails.ErrUnknownAlias, req.Alias)
	}

	if r.failNext > 0 {
		r.failNext--
		// Se registra la llamada: el riel PUDO haberla procesado aunque no
		// respondiera. Es justo la ambigüedad que obliga a reintentar con el
		// mismo TransferID en vez de generar uno nuevo.
		r.sentCallLog = append(r.sentCallLog, req)
		return nil, fmt.Errorf("%w: timeout", rails.ErrUnavailable)
	}

	if r.rejectNext > 0 {
		r.rejectNext--
		r.sentCallLog = append(r.sentCallLog, req)
		return nil, fmt.Errorf("%w: fondos del destinatario no disponibles", rails.ErrRejected)
	}

	receipt := &rails.SendReceipt{
		RailTransactionID: "rail-" + uuid.NewString(),
		AcceptedAt:        time.Now().UTC(),
	}
	r.sent[req.TransferID] = receipt
	r.sentCallLog = append(r.sentCallLog, req)
	return receipt, nil
}

func (r *Rail) wait(ctx context.Context) error {
	r.mu.Lock()
	latency := r.latency
	r.mu.Unlock()
	if latency == 0 {
		return nil
	}
	select {
	case <-time.After(latency):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ---------------------------------------------------------------- entrantes

// EmitCredit construye la notificación de una acreditación entrante.
func EmitCredit(toAlias string, amountMinor int64, currency, sender string) rails.InboundCredit {
	return rails.InboundCredit{
		RailTransactionID: "rail-" + uuid.NewString(),
		ToAlias:           toAlias,
		AmountMinor:       amountMinor,
		Currency:          currency,
		SenderName:        sender,
		OccurredAt:        time.Now().UTC(),
	}
}

// Duplicate reproduce el reenvío de una notificación ya entregada. Es el caso más
// común en producción: el riel no recibió el ACK a tiempo y vuelve a mandar.
func Duplicate(credit rails.InboundCredit) rails.InboundCredit { return credit }

// Shuffle devuelve las notificaciones en orden inverso, simulando la entrega
// desordenada de un riel con varias particiones o reintentos solapados.
func Shuffle(credits []rails.InboundCredit) []rails.InboundCredit {
	out := make([]rails.InboundCredit, len(credits))
	for i, c := range credits {
		out[len(credits)-1-i] = c
	}
	return out
}

// ---------------------------------------------------------------- resolutor

// AliasDirectory resuelve alias a cuentas internas. Sustituto en memoria del
// directorio de clientes.
type AliasDirectory struct {
	mu      sync.RWMutex
	byAlias map[string]string
}

func NewAliasDirectory() *AliasDirectory {
	return &AliasDirectory{byAlias: make(map[string]string)}
}

func (d *AliasDirectory) Register(alias, accountID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.byAlias[alias] = accountID
}

func (d *AliasDirectory) AccountIDForAlias(_ context.Context, alias string) (string, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	accountID, ok := d.byAlias[alias]
	if !ok {
		return "", fmt.Errorf("%w: %s", rails.ErrAliasNotOurs, alias)
	}
	return accountID, nil
}
