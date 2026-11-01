package forecast

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aibank/aibank/clients/go/coreclient"
	"github.com/aibank/aibank/services/credit"
)

// Service reúne lo que se sabe de la cuenta y proyecta.
type Service struct {
	core   *coreclient.Client
	cards  *credit.Service
	income credit.IncomeReader
	zone   *time.Location
	now    func() time.Time
}

// NewService arma la previsión. `cards` da los movimientos observados y el
// estado de cuenta de la tarjeta; `income`, el próximo ingreso según el motor de
// riesgo. `zone` es la del titular: «vence el 16» es el 16 en Lima.
func NewService(core *coreclient.Client, cards *credit.Service, income credit.IncomeReader, zone *time.Location) *Service {
	return &Service{core: core, cards: cards, income: income, zone: zone, now: time.Now}
}

// SetClock cambia el reloj, para las pruebas.
func (s *Service) SetClock(now func() time.Time) { s.now = now }

// Forecast proyecta el saldo de la cuenta del titular.
func (s *Service) Forecast(ctx context.Context, customerID, accountID string) (*Forecast, error) {
	now := s.now().In(s.zone)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, s.zone)

	balance, err := s.core.GetBalance(ctx, accountID)
	if err != nil {
		return nil, fmt.Errorf("saldo: %w", err)
	}
	real, synthetic, err := s.cards.Observed(ctx, accountID)
	if err != nil {
		return nil, fmt.Errorf("movimientos: %w", err)
	}

	in := Input{Today: today, BalanceMicros: balance.AmountMicros, Currency: balance.Currency}
	since := today.AddDate(0, 0, -SpendWindow)
	oldest := today
	for _, m := range real {
		if m.OccurredAt.Before(oldest) {
			oldest = m.OccurredAt
		}
		if m.OccurredAt.Before(since) {
			continue
		}
		in.Recent = append(in.Recent, Movement{At: m.OccurredAt, Out: m.Direction == "out",
			AmountMicros: m.AmountMicros, Kind: m.Kind})
	}
	in.HistoryDays = int(today.Sub(oldest).Hours() / 24)

	if in.Income, err = s.nextIncome(ctx, today, real, synthetic); err != nil {
		return nil, err
	}
	if in.Card, err = s.cardDue(ctx, customerID, today); err != nil {
		return nil, err
	}
	f := Project(in)
	return &f, nil
}

// nextIncome pregunta al motor por lo real. Solo si no ve nada, y en el sandbox,
// usa el sueldo de prueba, y lo marca: la persona tiene que saber que no es suyo.
func (s *Service) nextIncome(ctx context.Context, today time.Time, real, synthetic []credit.RiskMovement) (*Income, error) {
	found, err := s.income.NextIncome(ctx, today, real)
	isSynthetic := false
	if err == nil && found == nil && len(synthetic) > 0 {
		found, err = s.income.NextIncome(ctx, today, append(append([]credit.RiskMovement{}, real...), synthetic...))
		isSynthetic = found != nil
	}
	if err != nil {
		return nil, fmt.Errorf("próximo ingreso: %w", err)
	}
	if found == nil {
		return nil, nil
	}
	on, err := time.ParseInLocation(time.DateOnly, found.NextExpectedOn, s.zone)
	if err != nil {
		return nil, fmt.Errorf("fecha del próximo ingreso: %w", err)
	}
	return &Income{AmountMicros: found.TypicalAmountMicros, ExpectedOn: on, Cadence: found.Cadence,
		Synthetic: isSynthetic}, nil
}

// cardDue es lo que queda por pagar del último estado de cuenta. Nil sin tarjeta,
// sin estado de cuenta, o si ya se pagó entero.
func (s *Service) cardDue(ctx context.Context, customerID string, today time.Time) (*CardDue, error) {
	statements, err := s.cards.Statements(ctx, customerID, 1)
	if errors.Is(err, credit.ErrNoLine) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("estado de cuenta: %w", err)
	}
	if len(statements) == 0 {
		return nil, nil
	}
	st := statements[0]
	paid := st.GetPaidSinceCut().GetAmountMicros()
	total := st.GetClosing().GetAmountMicros() - paid
	if total <= 0 {
		return nil, nil
	}
	due, err := time.ParseInLocation(time.DateOnly, st.GetDueDate(), s.zone)
	if err != nil {
		return nil, fmt.Errorf("vencimiento: %w", err)
	}
	minimum := st.GetMinimumPayment().GetAmountMicros() - paid
	if minimum < 0 {
		minimum = 0
	}
	if due.Before(today) && minimum == 0 {
		// Venció con el mínimo pagado: lo que queda pasa al próximo estado de
		// cuenta, con sus intereses, y no se debe hoy.
		return nil, nil
	}
	return &CardDue{DueOn: due, MinimumMicros: minimum, TotalMicros: total,
		Overdue: due.Before(today) && minimum > 0}, nil
}
