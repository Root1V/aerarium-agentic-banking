// Package cards define el PUERTO de emisión y procesamiento de tarjetas.
//
// Una compra con tarjeta NO es un movimiento único: son dos momentos separados
// por horas o días, y confundirlos es una de las formas más rápidas de descuadrar
// un banco.
//
//  1. AUTORIZACIÓN. El comercio pregunta si hay fondos. El emisor tiene un par de
//     segundos para responder. Si aprueba, RETIENE el dinero — no lo cobra.
//  2. PRESENTACIÓN (clearing). Más tarde el comercio cobra de verdad, y el monto
//     puede NO coincidir con el autorizado: una propina en un restaurante o una
//     carga de combustible autorizan una cifra y cobran otra.
//
// Entre ambos momentos el dinero está retenido: ya no es gastable por el cliente,
// pero tampoco salió del banco. Modelarlo así es lo que permite que el saldo
// disponible que ve la persona sea el correcto.
package cards

import (
	"context"
	"errors"
	"time"
)

var (
	// ErrUnknownCard: la tarjeta no existe en el directorio.
	ErrUnknownCard = errors.New("tarjeta desconocida")
	// ErrUnknownAuthorization: se intenta cobrar o reversar algo que no se autorizó.
	ErrUnknownAuthorization = errors.New("autorización desconocida")
	// ErrProcessorUnavailable: el procesador no respondió. Reintentable.
	ErrProcessorUnavailable = errors.New("procesador de tarjetas no disponible")
)

// CardStatus refleja si la tarjeta puede operar.
type CardStatus int

const (
	CardActive CardStatus = iota
	// CardFrozen: congelada por la persona usuaria desde la app.
	CardFrozen
	// CardCancelled: baja definitiva.
	CardCancelled
)

// Card es una tarjeta emitida.
//
// Nunca contiene el número completo (PAN): ese dato vive solo en el procesador,
// que está certificado PCI. Guardarlo aquí metería a todo el sistema dentro del
// alcance de esa certificación sin ninguna ganancia.
type Card struct {
	ID string
	// AccountID es la cuenta que se debita.
	AccountID string
	// LastFour es lo único del número que se muestra o se guarda.
	LastFour string
	Status   CardStatus
}

// CardDirectory resuelve una tarjeta a su cuenta.
type CardDirectory interface {
	Card(ctx context.Context, cardID string) (*Card, error)
}

// ---------------------------------------------------------------- entrantes

// AuthorizationRequest es la consulta del procesador ante una compra.
//
// Llega de forma SÍNCRONA y con un plazo de respuesta de unos dos segundos: si no
// se contesta a tiempo, la red decide por el emisor. Por eso el camino de
// autorización hace lo mínimo — resolver la tarjeta y asentar la retención.
type AuthorizationRequest struct {
	// NetworkTransactionID identifica la compra en la red. Es la identidad que
	// enlaza la autorización con su cobro posterior, y la que se repite cuando la
	// red reintenta.
	NetworkTransactionID string
	CardID               string
	AmountMinor          int64
	Currency             string
	MerchantName         string
	RequestedAt          time.Time
}

// DeclineReason explica un rechazo. La red exige un motivo, no un "no".
type DeclineReason int

const (
	DeclineNone DeclineReason = iota
	DeclineInsufficientFunds
	DeclineCardFrozen
	DeclineCardCancelled
	DeclineUnknownCard
	DeclineLimitExceeded
	// DeclineSystemError: no se pudo decidir. Se rechaza en vez de aprobar a
	// ciegas: aprobar sin poder retener el dinero es regalar el importe.
	DeclineSystemError
)

func (r DeclineReason) String() string {
	switch r {
	case DeclineInsufficientFunds:
		return "insufficient_funds"
	case DeclineCardFrozen:
		return "card_frozen"
	case DeclineCardCancelled:
		return "card_cancelled"
	case DeclineUnknownCard:
		return "unknown_card"
	case DeclineLimitExceeded:
		return "limit_exceeded"
	case DeclineSystemError:
		return "system_error"
	default:
		return "none"
	}
}

// AuthorizationDecision es la respuesta a la red.
type AuthorizationDecision struct {
	Approved bool
	Reason   DeclineReason
	// HoldTransactionID identifica el asiento de retención.
	HoldTransactionID string
	// Duplicate indica que la red repitió una autorización ya resuelta.
	Duplicate bool
}

// ClearingNotification es el cobro efectivo del comercio.
type ClearingNotification struct {
	// NetworkTransactionID enlaza con la autorización previa.
	NetworkTransactionID string
	// ClearingID identifica este cobro; la red puede reenviarlo.
	ClearingID string
	// FinalAmountMinor puede diferir del autorizado: propinas, combustible,
	// cobros parciales.
	FinalAmountMinor int64
	Currency         string
	SettledAt        time.Time
}

// ReversalNotification anula una autorización sin cobro: el comercio canceló o
// la retención expiró.
type ReversalNotification struct {
	NetworkTransactionID string
	ReversalID           string
	Reason               string
}

// ---------------------------------------------------------------- salientes

// IssueCardRequest pide una tarjeta nueva al procesador.
type IssueCardRequest struct {
	AccountID string
	// HolderName tal como se imprime o se muestra.
	HolderName string
	Virtual    bool
}

// Processor es el emisor de tarjetas (Pomelo, Dock, ...).
type Processor interface {
	Name() string
	// IssueCard DEBE ser idempotente por reference.
	IssueCard(ctx context.Context, reference string, req IssueCardRequest) (*Card, error)
	// SetStatus congela o cancela una tarjeta.
	SetStatus(ctx context.Context, cardID string, status CardStatus) error
}
