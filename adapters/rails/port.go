// Package rails define el PUERTO de los rieles de pago instantáneo.
//
// Cada país tiene el suyo — PIX en Brasil, SPEI/DiMo en México, Bre-B en Colombia,
// la interoperabilidad de la CCE en Perú, Transferencias 3.0 en Argentina — y todos
// hacen lo mismo: resolver un alias a un destinatario y mover dinero. Esta interfaz
// es la que implementan tanto el simulador como los rieles reales, de modo que
// cambiar de país o de proveedor es escribir un adaptador, no tocar el negocio.
package rails

import (
	"context"
	"errors"
	"time"
)

// Errores que todo riel debe saber expresar.
var (
	// ErrUnknownAlias: el alias no existe. Definitivo, no reintentar.
	ErrUnknownAlias = errors.New("alias desconocido")
	// ErrRejected: el riel rechazó la operación (cuenta cerrada, límite, etc.).
	ErrRejected = errors.New("operación rechazada por el riel")
	// ErrUnavailable: el riel no respondió. Reintentable — pero OJO: puede haberse
	// procesado igual, así que el reintento debe llevar el mismo TransferID.
	ErrUnavailable = errors.New("riel no disponible")
)

// AliasTarget es el destinatario detrás de un alias (llave Pix, CVU, llave Bre-B).
type AliasTarget struct {
	Alias       string
	HolderName  string
	Institution string
}

// SendRequest es una orden de salida hacia el riel.
type SendRequest struct {
	// TransferID lo genera AIBank y es estable entre reintentos: es lo que impide
	// que un reenvío por timeout mande el dinero dos veces.
	TransferID  string
	Alias       string
	AmountMicros int64
	Currency    string
	Reference   string
}

// SendReceipt es la confirmación del riel.
type SendReceipt struct {
	// RailTransactionID identifica la operación en el sistema del riel.
	RailTransactionID string
	AcceptedAt        time.Time
}

// InboundCredit es una acreditación entrante notificada por el riel.
//
// Los rieles reenvían notificaciones ante cualquier duda, así que este mensaje
// llega DUPLICADO y FUERA DE ORDEN con normalidad. RailTransactionID es la
// identidad estable que permite tratarlo como idempotente.
type InboundCredit struct {
	RailTransactionID string
	ToAlias           string
	AmountMicros       int64
	Currency          string
	SenderName        string
	OccurredAt        time.Time
}

// Rail es el puerto: lo implementan el simulador y, más adelante, cada riel real.
type Rail interface {
	// Name identifica al riel en claves de idempotencia y trazas ("sim", "pix", "spei").
	Name() string
	ResolveAlias(ctx context.Context, alias string) (*AliasTarget, error)
	// Send envía dinero al exterior. DEBE ser idempotente por TransferID.
	Send(ctx context.Context, req SendRequest) (*SendReceipt, error)
}

// AccountResolver traduce un alias del riel a la cuenta interna del cliente.
//
// Hoy lo resuelve un mapa en memoria; en producción será el directorio de clientes.
// Está como puerto para que el servicio no dependa de dónde vive esa tabla.
type AccountResolver interface {
	AccountIDForAlias(ctx context.Context, alias string) (string, error)
}

// ErrAliasNotOurs: el alias no corresponde a ninguna cuenta de AIBank.
var ErrAliasNotOurs = errors.New("el alias no pertenece a este banco")
