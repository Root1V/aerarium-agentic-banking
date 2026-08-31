// Package sim simula el procesador y la red de tarjetas.
//
// Permite provocar a voluntad lo que en un sandbox real es difícil o imposible de
// reproducir: una propina que cobra más de lo autorizado, una carga de combustible
// que autoriza un dólar y cobra sesenta, un cobro que nunca llega, una red que
// reenvía la misma autorización.
package sim

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/aibank/aibank/adapters/cards"
	"github.com/google/uuid"
)

// Processor emite tarjetas en memoria y hace de directorio.
type Processor struct {
	mu    sync.RWMutex
	name  string
	byID  map[string]cards.Card
	issue map[string]string // reference -> cardID, para idempotencia
}

func NewProcessor(name string) *Processor {
	return &Processor{
		name:  name,
		byID:  make(map[string]cards.Card),
		issue: make(map[string]string),
	}
}

func (p *Processor) Name() string { return p.name }

func (p *Processor) IssueCard(_ context.Context, reference string, req cards.IssueCardRequest) (*cards.Card, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if cardID, ok := p.issue[reference]; ok {
		card := p.byID[cardID]
		return &card, nil
	}

	card := cards.Card{
		ID:        "card-" + uuid.NewString(),
		AccountID: req.AccountID,
		LastFour:  fmt.Sprintf("%04d", len(p.byID)%10000),
		Status:    cards.CardActive,
	}
	p.byID[card.ID] = card
	p.issue[reference] = card.ID
	return &card, nil
}

func (p *Processor) SetStatus(_ context.Context, cardID string, status cards.CardStatus) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	card, ok := p.byID[cardID]
	if !ok {
		return cards.ErrUnknownCard
	}
	card.Status = status
	p.byID[cardID] = card
	return nil
}

func (p *Processor) Card(_ context.Context, cardID string) (*cards.Card, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	card, ok := p.byID[cardID]
	if !ok {
		return nil, cards.ErrUnknownCard
	}
	return &card, nil
}

// ---------------------------------------------------------------- eventos

// Purchase representa una compra en curso, para encadenar sus tres momentos.
type Purchase struct {
	NetworkTransactionID string
	CardID               string
	AmountMinor          int64
	Currency             string
	MerchantName         string
}

// NewPurchase inicia una compra.
func NewPurchase(cardID string, amountMinor int64, currency, merchant string) Purchase {
	return Purchase{
		NetworkTransactionID: "net-" + uuid.NewString(),
		CardID:               cardID,
		AmountMinor:          amountMinor,
		Currency:             currency,
		MerchantName:         merchant,
	}
}

// Authorization es la consulta que la red hace al emisor.
func (p Purchase) Authorization() cards.AuthorizationRequest {
	return cards.AuthorizationRequest{
		NetworkTransactionID: p.NetworkTransactionID,
		CardID:               p.CardID,
		AmountMinor:          p.AmountMinor,
		Currency:             p.Currency,
		MerchantName:         p.MerchantName,
		RequestedAt:          time.Now().UTC(),
	}
}

// Clearing es el cobro efectivo. `finalAmountMinor` puede diferir del autorizado:
// así se reproducen la propina del restaurante o la carga de combustible.
func (p Purchase) Clearing(finalAmountMinor int64) cards.ClearingNotification {
	return cards.ClearingNotification{
		NetworkTransactionID: p.NetworkTransactionID,
		ClearingID:           "clr-" + uuid.NewString(),
		FinalAmountMinor:     finalAmountMinor,
		Currency:             p.Currency,
		SettledAt:            time.Now().UTC(),
	}
}

// Reversal anula la compra sin cobro.
func (p Purchase) Reversal(reason string) cards.ReversalNotification {
	return cards.ReversalNotification{
		NetworkTransactionID: p.NetworkTransactionID,
		ReversalID:           "rev-" + uuid.NewString(),
		Reason:               reason,
	}
}
