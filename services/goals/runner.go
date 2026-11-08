package goals

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/aibank/aibank/clients/go/coreclient"
	"github.com/aibank/aibank/services/control"
	"github.com/aibank/aibank/services/forecast"
	"github.com/aibank/aibank/services/pockets"
)

// noExpiry: la regla vale hasta que se quita o se cumple la meta. La capa de
// control pide una vigencia; esta es la de «sin fecha».
var noExpiry = time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)

// RunDue ejecuta las reglas a las que ya les tocó. Devuelve cuántas corrió. Una
// que falla por algo pasajero (la previsión o el core sin responder) se deja
// para la próxima vuelta, sin perder su periodo.
func (s *Service) RunDue(ctx context.Context) (int, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id FROM goals.rules WHERE revoked_at IS NULL AND next_run_on <= $1
		 ORDER BY next_run_on LIMIT 500`, s.today())
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	ran := 0
	var firstErr error
	for _, id := range ids {
		if _, err := s.RunRule(ctx, id); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		ran++
	}
	return ran, firstErr
}

// due es una regla viva con su meta, bloqueada para ejecutarla.
type due struct {
	rule Rule
	goal Goal
}

// RunRule ejecuta una regla si le toca. Devuelve lo que hizo, o nil si no le
// tocaba (o si otro proceso la está ejecutando).
func (s *Service) RunRule(ctx context.Context, ruleID string) (*Contribution, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	today := s.today()
	var d due
	var cadence string
	// SKIP LOCKED: dos procesos del canal no ejecutan la misma regla a la vez.
	err = tx.QueryRowContext(ctx, `
		SELECT r.id, r.amount_micros, r.cadence, r.anchor_day, r.next_run_on,
		       g.id, g.customer_id, g.account_id, g.pocket_id, g.name, g.currency, g.target_micros
		  FROM goals.rules r JOIN goals.goals g ON g.id = r.goal_id
		 WHERE r.id = $1 AND r.revoked_at IS NULL AND r.next_run_on <= $2 AND g.closed_at IS NULL
		   FOR UPDATE OF r SKIP LOCKED`, ruleID, today).
		Scan(&d.rule.ID, &d.rule.AmountMicros, &cadence, &d.rule.AnchorDay, &d.rule.NextRunOn,
			&d.goal.ID, &d.goal.CustomerID, &d.goal.AccountID, &d.goal.PocketID, &d.goal.Name, &d.goal.Currency,
			&d.goal.TargetMicros)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	d.rule.Cadence = Cadence(cadence)
	d.rule.GoalID = d.goal.ID

	c := Contribution{Period: periodOf(d.rule.Cadence, today), RunOn: today, AskedMicros: d.rule.AmountMicros}
	next := nextRun(d.rule.Cadence, d.rule.AnchorDay, today)

	// Un periodo, un aporte: si ya se hizo (el proceso murió antes de mover la
	// fecha), solo se avanza.
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM goals.contributions
		 WHERE rule_id = $1 AND period = $2)`, d.rule.ID, c.Period).Scan(&exists); err != nil {
		return nil, err
	}
	if exists {
		if err := advance(ctx, tx, d.rule.ID, next); err != nil {
			return nil, err
		}
		return nil, tx.Commit()
	}

	saved, err := s.pockets.Balance(ctx, d.goal.CustomerID, d.goal.PocketID)
	if errors.Is(err, pockets.ErrNotFound) {
		// El bolsillo se cerró sin pasar por la meta: la regla ya no tiene dónde apartar.
		if _, err := tx.ExecContext(ctx, `UPDATE goals.rules SET revoked_at = now(), revoked_reason = $2
			 WHERE id = $1`, d.rule.ID, EndedByClosed); err != nil {
			return nil, err
		}
		return nil, tx.Commit()
	}
	if err != nil {
		return nil, err
	}

	if err := s.contribute(ctx, d, saved, &c); err != nil {
		return nil, err
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO goals.contributions (rule_id, period, run_on, outcome, asked_micros, amount_micros, reason,
		                                 lowest_micros, income_on, transaction_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NULLIF($10, '')::uuid)`,
		d.rule.ID, c.Period, c.RunOn, c.Outcome, c.AskedMicros, c.AmountMicros, c.Reason,
		c.LowestMicros, c.IncomeOn, c.TransactionID); err != nil {
		return nil, err
	}
	if c.Outcome == OutcomeReached || saved+c.AmountMicros >= d.goal.TargetMicros {
		// Cumplida: la regla no sigue apartando. La meta queda, con su dinero.
		if _, err := tx.ExecContext(ctx, `UPDATE goals.rules SET revoked_at = now(), revoked_reason = $2
			 WHERE id = $1`, d.rule.ID, EndedByReached); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE goals.goals SET reached_at = now()
			 WHERE id = $1 AND reached_at IS NULL`, d.goal.ID); err != nil {
			return nil, err
		}
	} else if err := advance(ctx, tx, d.rule.ID, next); err != nil {
		return nil, err
	}
	c.CreatedAt = s.now()
	return &c, tx.Commit()
}

// contribute decide cuánto apartar y lo aparta. Deja en `c` lo que hizo. Un
// error es pasajero: el periodo se reintenta.
func (s *Service) contribute(ctx context.Context, d due, saved int64, c *Contribution) error {
	remaining := d.goal.TargetMicros - saved
	if remaining <= 0 {
		c.Outcome = OutcomeReached
		return nil
	}
	amount := min(d.rule.AmountMicros, remaining)

	if s.forecast != nil {
		f, err := s.forecast.Forecast(ctx, d.goal.CustomerID, d.goal.AccountID)
		if err != nil {
			// Sin previsión no se aparta a ciegas: se reintenta en la próxima vuelta.
			return fmt.Errorf("previsión de saldo: %w", err)
		}
		lowest := f.Lowest.AmountMicros
		c.LowestMicros = &lowest
		if f.Income != nil {
			on := f.Income.ExpectedOn
			c.IncomeOn = &on
		}
		// Lo que se puede apartar sin bajar del colchón: lo que se gasta en unos
		// días, en el punto más bajo antes del próximo ingreso.
		safe := lowest - forecast.TightDays*f.DailySpendMicros
		if safe < amount {
			amount = max(0, safe) / unit * unit
			c.Reason = ReasonKeepsCushion
		}
	}
	if amount <= 0 {
		c.Outcome = OutcomeSkipped
		return nil
	}

	actor := control.Actor{Kind: control.ActorRule, ID: d.rule.ID}
	maxPerOperation := d.rule.AmountMicros
	req := control.Request{
		CustomerID: d.goal.CustomerID, Actor: actor, Action: control.ActionAutoSave,
		AmountMicros: amount, Counterparty: d.goal.PocketID,
		Operation: []byte("auto_save" +
			"\nrule=" + d.rule.ID +
			"\ngoal=" + d.goal.ID +
			"\npocket=" + d.goal.PocketID +
			"\namount=" + strconv.FormatInt(amount, 10) +
			"\ncurrency=" + d.goal.Currency +
			"\nperiod=" + c.Period),
		Grant: &control.Grant{Ref: "rule:" + d.rule.ID, Level: control.LevelMandate,
			Actions: []control.Action{control.ActionAutoSave}, MaxPerOperation: &maxPerOperation,
			ExpiresAt: noExpiry},
	}
	decision, err := s.control.Decide(ctx, req)
	if err != nil {
		return err
	}
	if decision.Outcome != control.Allow {
		c.Outcome, c.Reason = OutcomeSkipped, ReasonNotAllowed
		return nil
	}

	moved, err := s.pockets.Move(ctx, d.goal.CustomerID, d.goal.PocketID, "goal:"+d.rule.ID+":"+c.Period,
		true, amount)
	if errors.Is(err, coreclient.ErrInsufficientFunds) {
		c.Outcome, c.Reason = OutcomeSkipped, ReasonInsufficientFunds
		return nil
	}
	if err != nil {
		return err
	}
	if err := s.control.Executed(ctx, d.goal.CustomerID, moved.TransactionID, control.Authorization{
		Actor: actor, Action: control.ActionAutoSave, Outcome: "allow",
		Evidence: "rule:" + d.rule.ID + ";goal:" + d.goal.ID,
	}); err != nil {
		return err
	}
	c.AmountMicros, c.TransactionID = amount, moved.TransactionID
	c.Outcome = OutcomeSaved
	if amount < c.AskedMicros && c.Reason != "" {
		c.Outcome = OutcomeReduced
	}
	return nil
}

func advance(ctx context.Context, tx *sql.Tx, ruleID string, next time.Time) error {
	_, err := tx.ExecContext(ctx, `UPDATE goals.rules SET next_run_on = $2 WHERE id = $1`, ruleID, next)
	return err
}

// periodOf es la semana ISO o el mes del día: un aporte por periodo.
func periodOf(c Cadence, day time.Time) string {
	if c == Weekly {
		year, week := day.ISOWeek()
		return fmt.Sprintf("%d-W%02d", year, week)
	}
	return day.Format("2006-01")
}

// nextRun es el próximo día de aporte después de `day`.
func nextRun(c Cadence, anchorDay int, day time.Time) time.Time {
	if c == Weekly {
		return day.AddDate(0, 0, 7)
	}
	first := time.Date(day.Year(), day.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0)
	last := first.AddDate(0, 1, -1).Day()
	return first.AddDate(0, 0, min(anchorDay, last)-1)
}
