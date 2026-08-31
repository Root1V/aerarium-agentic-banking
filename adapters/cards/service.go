package cards

import (
	"context"
	"errors"
	"fmt"

	"github.com/aibank/aibank/clients/go/coreclient"
)

// Accounts son las cuentas internas del programa de tarjetas.
//
// Los tipos contables no son intercambiables:
//
//   - AuthHold debe ser PASIVO. El dinero retenido salió del alcance del cliente
//     pero SIGUE dentro del banco: es una obligación, no un activo del banco.
//   - Settlement debe ser ACTIVO. Es la posición frente a la red; baja cuando el
//     dinero sale de verdad al cobrarse la compra.
//   - Overage debe ser ACTIVO. Es cartera por cobrar: lo que el banco adelantó
//     cuando el cobro final superó lo retenido y el cliente no tenía saldo.
type Accounts struct {
	AuthHoldID   string
	SettlementID string
	OverageID    string
}

// Service traduce entre la red de tarjetas y el ledger.
type Service struct {
	core      *coreclient.Client
	store     *Store
	directory CardDirectory
	accounts  Accounts
	network   string
}

func NewService(core *coreclient.Client, store *Store, directory CardDirectory, accounts Accounts, network string) *Service {
	return &Service{core: core, store: store, directory: directory, accounts: accounts, network: network}
}

// ---------------------------------------------------------------- autorizar

// Authorize decide si aprobar una compra y retiene el dinero.
//
// Corre bajo el plazo de la red (unos dos segundos), así que hace lo mínimo:
// resolver la tarjeta y asentar la retención. Cualquier trabajo adicional —
// antifraude pesado, notificaciones, analítica— pertenece a un consumidor de
// eventos, no a este camino.
//
// Ante cualquier duda se RECHAZA. Aprobar sin haber podido retener el dinero es
// regalar el importe: el comercio cobra igual y el banco no tiene con qué.
func (s *Service) Authorize(ctx context.Context, req AuthorizationRequest) (*AuthorizationDecision, error) {
	if req.NetworkTransactionID == "" {
		return nil, errors.New("la autorización no trae identificador de red")
	}
	if req.AmountMinor <= 0 {
		return nil, fmt.Errorf("monto inválido: %d", req.AmountMinor)
	}

	card, err := s.directory.Card(ctx, req.CardID)
	if err != nil {
		if errors.Is(err, ErrUnknownCard) {
			return &AuthorizationDecision{Reason: DeclineUnknownCard}, nil
		}
		return &AuthorizationDecision{Reason: DeclineSystemError}, nil
	}

	switch card.Status {
	case CardFrozen:
		return &AuthorizationDecision{Reason: DeclineCardFrozen}, nil
	case CardCancelled:
		return &AuthorizationDecision{Reason: DeclineCardCancelled}, nil
	}

	created, existing, err := s.store.reserve(ctx, Authorization{
		NetworkTransactionID: req.NetworkTransactionID,
		CardID:               req.CardID,
		AccountID:            card.AccountID,
		AmountMinor:          req.AmountMinor,
		Currency:             req.Currency,
		MerchantName:         req.MerchantName,
	})
	if err != nil {
		return &AuthorizationDecision{Reason: DeclineSystemError}, nil
	}

	// La red repite autorizaciones cuando no recibe respuesta a tiempo. Se
	// responde lo mismo sin retener dinero de nuevo.
	if !created {
		return &AuthorizationDecision{
			Approved:          existing.isHeld() || existing.Status == statusCleared,
			HoldTransactionID: existing.HoldTransactionID,
			Duplicate:         true,
		}, nil
	}

	// Retención: el dinero deja de estar disponible para el cliente pero sigue
	// dentro del banco. Aquí es donde el core rechaza por falta de fondos.
	result, err := s.core.Post(ctx, s.authKey(req.NetworkTransactionID), "card_authorization_hold",
		[]coreclient.Entry{
			coreclient.Debit(card.AccountID, req.AmountMinor, req.Currency),
			coreclient.Credit(s.accounts.AuthHoldID, req.AmountMinor, req.Currency),
		},
		req.MerchantName,
	)
	if err != nil {
		// No se retuvo nada: la autorización no debe quedar registrada como viva,
		// o el cliente vería retenido un dinero que nunca se movió.
		if dropErr := s.store.drop(ctx, req.NetworkTransactionID); dropErr != nil {
			return nil, fmt.Errorf("descartar autorización fallida: %w", dropErr)
		}
		switch {
		case errors.Is(err, coreclient.ErrInsufficientFunds):
			return &AuthorizationDecision{Reason: DeclineInsufficientFunds}, nil
		case errors.Is(err, coreclient.ErrTransactionCapExceeded),
			errors.Is(err, coreclient.ErrBalanceCapExceeded):
			return &AuthorizationDecision{Reason: DeclineLimitExceeded}, nil
		default:
			return &AuthorizationDecision{Reason: DeclineSystemError}, nil
		}
	}

	if err := s.store.markHeld(ctx, req.NetworkTransactionID, result.TransactionID); err != nil {
		return nil, err
	}

	return &AuthorizationDecision{Approved: true, HoldTransactionID: result.TransactionID}, nil
}

// ---------------------------------------------------------------- cobrar

// ClearingResult describe cómo quedó el cobro.
type ClearingResult struct {
	TransactionID string
	// FinalAmountMinor es lo efectivamente cobrado.
	FinalAmountMinor int64
	// ReturnedMinor es lo que se devuelve al cliente cuando el comercio cobra
	// menos de lo autorizado.
	ReturnedMinor int64
	// OverageMinor es el excedente que el banco adelantó porque el cliente no
	// tenía saldo para cubrir la diferencia. Es cartera por cobrar.
	OverageMinor int64
	Duplicate    bool
}

// Clear asienta el cobro efectivo del comercio.
//
// El monto final puede diferir del autorizado y eso es normal, no un error: una
// propina o una carga de combustible autorizan una cifra y cobran otra. Todo se
// resuelve en UNA transacción contable:
//
//	debe  retención (lo autorizado)      -> libera la retención
//	haber posición frente a la red (final) -> el dinero sale del banco
//	y la diferencia contra el cliente: se le devuelve si cobraron menos, se le
//	cobra si cobraron más.
func (s *Service) Clear(ctx context.Context, notification ClearingNotification) (*ClearingResult, error) {
	if notification.ClearingID == "" {
		return nil, errors.New("el cobro no trae identificador")
	}
	if notification.FinalAmountMinor <= 0 {
		return nil, fmt.Errorf("monto final inválido: %d", notification.FinalAmountMinor)
	}

	auth, err := s.store.Get(ctx, notification.NetworkTransactionID)
	if err != nil {
		return nil, err
	}
	if auth.Status == statusCleared {
		// La red reenvió un cobro ya aplicado.
		return &ClearingResult{
			FinalAmountMinor: auth.ClearedAmountMinor,
			OverageMinor:     auth.OverageMinor,
			Duplicate:        true,
		}, nil
	}
	if auth.Status == statusReversed {
		return nil, fmt.Errorf("no se puede cobrar una autorización reversada: %s", auth.NetworkTransactionID)
	}

	final := notification.FinalAmountMinor
	currency := auth.Currency
	key := s.clearKey(notification.ClearingID)

	entries := []coreclient.Entry{
		coreclient.Debit(s.accounts.AuthHoldID, auth.AmountMinor, currency),
		coreclient.Credit(s.accounts.SettlementID, final, currency),
	}

	var returned, overage int64
	switch {
	case final < auth.AmountMinor:
		returned = auth.AmountMinor - final
		entries = append(entries, coreclient.Credit(auth.AccountID, returned, currency))
	case final > auth.AmountMinor:
		overage = final - auth.AmountMinor
		entries = append(entries, coreclient.Debit(auth.AccountID, overage, currency))
	}

	result, err := s.core.Post(ctx, key, "card_clearing", entries, auth.MerchantName)

	// El excedente sobre lo retenido puede no tener respaldo en el saldo. Rechazar
	// el cobro NO es una opción: el dinero ya se gastó en el comercio y la red va a
	// exigirlo igual. El banco lo adelanta contra cartera por cobrar y queda
	// registrado como tal.
	absorbed := false
	if err != nil && overage > 0 && errors.Is(err, coreclient.ErrInsufficientFunds) {
		entries[len(entries)-1] = coreclient.Debit(s.accounts.OverageID, overage, currency)
		result, err = s.core.Post(ctx, key, "card_clearing", entries, auth.MerchantName)
		absorbed = true
	}
	if err != nil {
		return nil, fmt.Errorf("asentar cobro: %w", err)
	}

	if err := s.store.markCleared(ctx, auth.NetworkTransactionID, final, boolTo(absorbed, overage)); err != nil {
		return nil, err
	}

	return &ClearingResult{
		TransactionID:    result.TransactionID,
		FinalAmountMinor: final,
		ReturnedMinor:    returned,
		OverageMinor:     boolTo(absorbed, overage),
		Duplicate:        result.Replayed,
	}, nil
}

// ---------------------------------------------------------------- reversar

// Reverse libera una retención sin cobro: el comercio anuló o la retención venció.
//
// Es la contraparte imprescindible de la autorización: sin ella, el dinero de una
// compra que nunca se cobró quedaría inmovilizado indefinidamente.
func (s *Service) Reverse(ctx context.Context, notification ReversalNotification) error {
	auth, err := s.store.Get(ctx, notification.NetworkTransactionID)
	if err != nil {
		return err
	}
	if auth.Status == statusReversed {
		return nil // reenvío de una reversa ya aplicada
	}
	if auth.Status == statusCleared {
		return fmt.Errorf("no se puede reversar una autorización ya cobrada: %s", auth.NetworkTransactionID)
	}

	if _, err := s.core.Post(ctx, s.reverseKey(notification.ReversalID), "card_authorization_reversal",
		[]coreclient.Entry{
			coreclient.Debit(s.accounts.AuthHoldID, auth.AmountMinor, auth.Currency),
			coreclient.Credit(auth.AccountID, auth.AmountMinor, auth.Currency),
		},
		notification.Reason,
	); err != nil {
		return fmt.Errorf("liberar retención: %w", err)
	}

	return s.store.markReversed(ctx, auth.NetworkTransactionID)
}

func boolTo(condition bool, value int64) int64 {
	if condition {
		return value
	}
	return 0
}

// Claves de idempotencia: llevan la red y la fase para que las tres etapas de una
// misma compra nunca colisionen entre sí.
func (s *Service) authKey(networkTxID string) string {
	return fmt.Sprintf("card:%s:auth:%s", s.network, networkTxID)
}
func (s *Service) clearKey(clearingID string) string {
	return fmt.Sprintf("card:%s:clear:%s", s.network, clearingID)
}
func (s *Service) reverseKey(reversalID string) string {
	return fmt.Sprintf("card:%s:reverse:%s", s.network, reversalID)
}
