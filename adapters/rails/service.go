package rails

import (
	"context"
	"errors"
	"fmt"

	"github.com/aibank/aibank/clients/go/coreclient"
)

// Accounts son las cuentas internas que el adaptador usa para representar
// contablemente su relación con el riel.
//
// Los tipos contables NO son intercambiables:
//
//   - Settlement debe ser ACTIVO. Es lo que el banco tiene en el riel: sube
//     cuando entra dinero de fuera y baja cuando sale.
//   - InTransit debe ser PASIVO. El dinero que dejó la cuenta del cliente pero
//     que el riel aún no confirmó SIGUE dentro del banco: no es un activo que
//     se tenga, es una obligación pendiente de pagar. Tiparlo como activo daría
//     saldos negativos y ocultaría el limbo en vez de mostrarlo.
type Accounts struct {
	SettlementID string
	// InTransit hace visible, en todo momento, cuánto dinero está en un limbo
	// entre el cliente y el mundo exterior.
	InTransitID string
}

// Service traduce entre el riel y el ledger.
type Service struct {
	core     *coreclient.Client
	rail     Rail
	resolver AccountResolver
	accounts Accounts
}

func NewService(core *coreclient.Client, rail Rail, resolver AccountResolver, accounts Accounts) *Service {
	return &Service{core: core, rail: rail, resolver: resolver, accounts: accounts}
}

// ---------------------------------------------------------------- entrante

// InboundResult describe el efecto de una acreditación entrante.
type InboundResult struct {
	TransactionID string
	AccountID     string
	// Duplicate indica que esa acreditación ya se había aplicado. No es un error:
	// es un reenvío del riel absorbido correctamente.
	Duplicate bool
}

// HandleInboundCredit acredita a un cliente el dinero que llegó por el riel.
//
// La clave de idempotencia se deriva del identificador que asigna EL RIEL, no de
// uno propio: así, cuando el riel reenvía la misma notificación —lo hace a
// diario—, el core reconoce la operación y no acredita dos veces.
func (s *Service) HandleInboundCredit(ctx context.Context, credit InboundCredit) (*InboundResult, error) {
	if credit.RailTransactionID == "" {
		return nil, errors.New("la notificación no trae identificador del riel")
	}
	if credit.AmountMicros <= 0 {
		return nil, fmt.Errorf("monto inválido: %d", credit.AmountMicros)
	}

	accountID, err := s.resolver.AccountIDForAlias(ctx, credit.ToAlias)
	if err != nil {
		// El alias no es nuestro: no se asienta nada. Devolver el dinero al riel
		// es una decisión de producto que vive fuera de este adaptador.
		return nil, err
	}

	result, err := s.core.Post(ctx, s.inboundKey(credit.RailTransactionID), "rail_inbound_credit",
		[]coreclient.Entry{
			coreclient.Debit(s.accounts.SettlementID, credit.AmountMicros, credit.Currency),
			coreclient.Credit(accountID, credit.AmountMicros, credit.Currency),
		},
		fmt.Sprintf("%s: %s", s.rail.Name(), credit.SenderName),
	)
	if err != nil {
		return nil, fmt.Errorf("acreditar entrada del riel: %w", err)
	}

	return &InboundResult{
		TransactionID: result.TransactionID,
		AccountID:     accountID,
		Duplicate:     result.Replayed,
	}, nil
}

// ---------------------------------------------------------------- saliente

// OutboundStatus es el desenlace de una transferencia saliente.
type OutboundStatus int

const (
	// OutboundSettled: el riel confirmó y el dinero salió de forma definitiva.
	OutboundSettled OutboundStatus = iota
	// OutboundReversed: el riel rechazó y se devolvió el dinero al cliente.
	OutboundReversed
	// OutboundInTransit: el riel no respondió. El dinero NO vuelve al cliente
	// automáticamente porque la operación pudo haberse procesado allá; queda
	// retenido hasta que la conciliación resuelva el caso.
	OutboundInTransit
)

func (s OutboundStatus) String() string {
	switch s {
	case OutboundSettled:
		return "settled"
	case OutboundReversed:
		return "reversed"
	default:
		return "in_transit"
	}
}

// OutboundRequest es una orden de envío de un cliente hacia fuera.
type OutboundRequest struct {
	// TransferID identifica la operación de negocio. Debe ser estable entre
	// reintentos del usuario o del sistema.
	TransferID    string
	FromAccountID string
	ToAlias       string
	AmountMicros   int64
	Currency      string
	Reference     string
}

// OutboundResult describe cómo quedó la transferencia.
type OutboundResult struct {
	Status            OutboundStatus
	RailTransactionID string
	// Reason explica un rechazo o una indefinición.
	Reason string
}

// SendOutbound envía dinero de un cliente hacia el exterior.
//
// El orden importa y es deliberado:
//
//  1. Se asienta primero el débito al cliente contra la cuenta de tránsito.
//     Si se llamara al riel primero y luego fallara el asiento, el dinero habría
//     salido del banco sin registro contable — una pérdida real, no un bug de datos.
//  2. Recién entonces se llama al riel.
//  3. Confirmado: el tránsito se salda contra la posición del riel.
//     Rechazado: se revierte y el cliente recupera su dinero.
//     Sin respuesta: NO se revierte. Ver OutboundInTransit.
func (s *Service) SendOutbound(ctx context.Context, req OutboundRequest) (*OutboundResult, error) {
	if req.TransferID == "" {
		return nil, errors.New("TransferID es obligatorio: sin él no hay idempotencia")
	}
	if req.AmountMicros <= 0 {
		return nil, fmt.Errorf("monto inválido: %d", req.AmountMicros)
	}

	// Validar el destino antes de mover dinero evita una reversa innecesaria.
	if _, err := s.rail.ResolveAlias(ctx, req.ToAlias); err != nil {
		return nil, err
	}

	// (1) Reserva: el dinero sale del cliente y queda retenido en tránsito.
	// Aquí es donde el core rechaza por fondos insuficientes o por tope, ANTES
	// de que el riel se entere de nada.
	if _, err := s.core.Post(ctx, s.reserveKey(req.TransferID), "rail_outbound_reserve",
		[]coreclient.Entry{
			coreclient.Debit(req.FromAccountID, req.AmountMicros, req.Currency),
			coreclient.Credit(s.accounts.InTransitID, req.AmountMicros, req.Currency),
		},
		fmt.Sprintf("%s -> %s", s.rail.Name(), req.ToAlias),
	); err != nil {
		return nil, fmt.Errorf("reservar fondos: %w", err)
	}

	// (2) Orden al riel, con el mismo TransferID en cada reintento.
	receipt, err := s.rail.Send(ctx, SendRequest{
		TransferID:  req.TransferID,
		Alias:       req.ToAlias,
		AmountMicros: req.AmountMicros,
		Currency:    req.Currency,
		Reference:   req.Reference,
	})

	switch {
	case err == nil:
		// (3a) Confirmado: el tránsito se salda contra la posición del riel.
		if _, postErr := s.core.Post(ctx, s.settleKey(req.TransferID), "rail_outbound_settle",
			[]coreclient.Entry{
				coreclient.Debit(s.accounts.InTransitID, req.AmountMicros, req.Currency),
				coreclient.Credit(s.accounts.SettlementID, req.AmountMicros, req.Currency),
			},
			receipt.RailTransactionID,
		); postErr != nil {
			// El dinero ya salió; el asiento de cierre se reintenta. Queda en
			// tránsito, que es exactamente lo que la conciliación debe detectar.
			return &OutboundResult{
				Status:            OutboundInTransit,
				RailTransactionID: receipt.RailTransactionID,
				Reason:            fmt.Sprintf("enviado pero sin asentar el cierre: %v", postErr),
			}, nil
		}
		return &OutboundResult{Status: OutboundSettled, RailTransactionID: receipt.RailTransactionID}, nil

	case errors.Is(err, ErrRejected), errors.Is(err, ErrUnknownAlias):
		// (3b) Rechazo explícito: el riel garantiza que no movió nada. Se revierte.
		if _, postErr := s.core.Post(ctx, s.reverseKey(req.TransferID), "rail_outbound_reverse",
			[]coreclient.Entry{
				coreclient.Debit(s.accounts.InTransitID, req.AmountMicros, req.Currency),
				coreclient.Credit(req.FromAccountID, req.AmountMicros, req.Currency),
			},
			fmt.Sprintf("reversa: %v", err),
		); postErr != nil {
			return nil, fmt.Errorf("revertir tras rechazo del riel: %w", postErr)
		}
		return &OutboundResult{Status: OutboundReversed, Reason: err.Error()}, nil

	default:
		// (3c) Sin respuesta: desenlace AMBIGUO. Revertir aquí sería el error caro:
		// si el riel sí procesó la orden, el cliente recuperaría un dinero que ya
		// salió y el banco asumiría la pérdida. Se deja en tránsito.
		return &OutboundResult{
			Status: OutboundInTransit,
			Reason: err.Error(),
		}, nil
	}
}

// Claves de idempotencia. Llevan el nombre del riel y la fase, de modo que las
// tres fases de una misma transferencia nunca colisionen entre sí.
func (s *Service) inboundKey(railTxID string) string {
	return fmt.Sprintf("rail:%s:in:%s", s.rail.Name(), railTxID)
}
func (s *Service) reserveKey(transferID string) string {
	return fmt.Sprintf("rail:%s:out-reserve:%s", s.rail.Name(), transferID)
}
func (s *Service) settleKey(transferID string) string {
	return fmt.Sprintf("rail:%s:out-settle:%s", s.rail.Name(), transferID)
}
func (s *Service) reverseKey(transferID string) string {
	return fmt.Sprintf("rail:%s:out-reverse:%s", s.rail.Name(), transferID)
}
