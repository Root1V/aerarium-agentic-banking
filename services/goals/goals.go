// Package goals maneja las metas con ahorro automático autorizado (doc 15, F3-03).
//
// Una meta es un bolsillo con un monto y, si se quiere, una fecha. Su regla de
// ahorro aparta en él cada semana o cada mes, y es un permiso como los demás: la
// persona la confirma con la cara, la ve en «Permisos y privacidad» y la quita
// cuando quiere. Cada aporte pasa por la capa de control con la regla como actor,
// y queda enlazado al movimiento que produjo.
//
// Lo que la distingue de un débito programado es que mira antes de apartar. Si
// la previsión de saldo dice que el aporte dejaría a la persona sin colchón antes
// de su próximo ingreso, aparta menos o no aparta, y dice por qué. Ahorrar no
// puede ser la causa de quedarse corto. Y al cumplirse la meta, la regla se
// detiene sola.
package goals

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"strings"
	"time"

	"github.com/aibank/aibank/services/control"
	"github.com/aibank/aibank/services/forecast"
	"github.com/aibank/aibank/services/pockets"
	"github.com/google/uuid"
	"github.com/lib/pq"
)

//go:embed schema.sql
var schema string

// Cadence es cada cuánto aparta la regla.
type Cadence string

const (
	Weekly  Cadence = "weekly"
	Monthly Cadence = "monthly"
)

// Lo que hizo la regla en un periodo.
const (
	OutcomeSaved   = "saved"
	OutcomeReduced = "reduced"
	OutcomeSkipped = "skipped"
	OutcomeReached = "reached"
)

// Por qué apartó menos o nada.
const (
	ReasonKeepsCushion      = "keeps_cushion"
	ReasonInsufficientFunds = "insufficient_funds"
	ReasonNotAllowed        = "not_allowed"
)

// Por qué terminó una regla.
const (
	EndedByHolder  = "holder"
	EndedByReached = "goal_reached"
	EndedByClosed  = "goal_closed"
)

// unit es lo mínimo que se aparta: un sol o un dólar. Apartar céntimos para no
// pasarse del colchón sería ruido en el historial.
const unit = 1_000_000

var (
	ErrNotFound   = errors.New("esa meta no existe")
	ErrBadName    = errors.New("el nombre es obligatorio, de hasta 40 caracteres")
	ErrBadTarget  = errors.New("el monto de la meta tiene que ser mayor que cero")
	ErrBadDate    = errors.New("la fecha de la meta tiene que ser futura")
	ErrBadRule    = errors.New("la regla aparta un monto mayor que cero, cada semana o cada mes")
	ErrRuleTooBig = errors.New("la regla no puede apartar más de lo que falta para la meta")
	ErrClosed     = errors.New("la meta ya está cerrada")
)

// Forecaster es la previsión de saldo (F3-01).
type Forecaster interface {
	Forecast(ctx context.Context, customerID, accountID string) (*forecast.Forecast, error)
}

// Goal es una meta con lo juntado hasta ahora.
type Goal struct {
	ID           string
	CustomerID   string
	AccountID    string
	PocketID     string
	Name         string
	Currency     string
	TargetMicros int64
	// TargetDate: para cuándo. Nil: sin fecha.
	TargetDate  *time.Time
	CreatedAt   time.Time
	ReachedAt   *time.Time
	SavedMicros int64
	// Rule: la regla viva, o nil.
	Rule *Rule
}

// Rule es la regla de ahorro de una meta.
type Rule struct {
	ID           string
	GoalID       string
	AmountMicros int64
	Cadence      Cadence
	AnchorDay    int
	NextRunOn    time.Time
	CreatedAt    time.Time
}

// Contribution es lo que hizo la regla en un periodo.
type Contribution struct {
	Period        string
	RunOn         time.Time
	Outcome       string
	AskedMicros   int64
	AmountMicros  int64
	Reason        string
	LowestMicros  *int64
	IncomeOn      *time.Time
	TransactionID string
	CreatedAt     time.Time
}

// Service guarda las metas y ejecuta sus reglas.
type Service struct {
	db       *sql.DB
	pockets  *pockets.Service
	control  *control.Service
	forecast Forecaster
	zone     *time.Location
	now      func() time.Time
}

// NewService arma las metas sobre los bolsillos y la capa de control. `zone` es
// la del titular: el aporte del lunes es el lunes en Lima.
func NewService(db *sql.DB, pk *pockets.Service, ctrl *control.Service, zone *time.Location) *Service {
	return &Service{db: db, pockets: pk, control: ctrl, zone: zone, now: time.Now}
}

// WithForecast hace que cada aporte mire antes la previsión de saldo. Sin ella,
// la regla solo se detiene si no hay saldo.
func (s *Service) WithForecast(f Forecaster) *Service {
	s.forecast = f
	return s
}

// SetClock cambia el reloj, para las pruebas.
func (s *Service) SetClock(now func() time.Time) { s.now = now }

func (s *Service) Migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, schema)
	return err
}

// today es el día de hoy en la zona del titular, como fecha (medianoche UTC):
// así se compara con las columnas DATE sin que la hora la corra un día.
func (s *Service) today() time.Time {
	now := s.now().In(s.zone)
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
}

// Create abre la meta y su bolsillo en una cuenta del cliente.
func (s *Service) Create(ctx context.Context, customerID, accountID, name string, targetMicros int64, targetDate *time.Time) (*Goal, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 40 {
		return nil, ErrBadName
	}
	if targetMicros <= 0 {
		return nil, ErrBadTarget
	}
	if targetDate != nil && !targetDate.After(s.today()) {
		return nil, ErrBadDate
	}
	pocket, err := s.pockets.Open(ctx, customerID, accountID, name, pockets.PurposeGoal)
	if err != nil {
		return nil, err
	}
	id := uuid.NewString()
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO goals.goals (id, customer_id, account_id, pocket_id, name, currency, target_micros, target_date)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		id, customerID, accountID, pocket.ID, name, pocket.Currency, targetMicros, targetDate)
	if err != nil {
		// Sin la meta, el bolsillo quedaría como uno cualquiera con propósito de meta.
		_ = s.pockets.Close(ctx, customerID, pocket.ID)
		return nil, err
	}
	return s.Get(ctx, customerID, id)
}

const goalColumns = `g.id, g.customer_id, g.account_id, g.pocket_id, g.name, g.currency, g.target_micros,
	g.target_date, g.created_at, g.reached_at,
	r.id, r.amount_micros, r.cadence, r.anchor_day, r.next_run_on, r.created_at`

const goalFrom = ` FROM goals.goals g
	LEFT JOIN goals.rules r ON r.goal_id = g.id AND r.revoked_at IS NULL`

func scanGoal(row interface{ Scan(...any) error }) (*Goal, error) {
	var g Goal
	var ruleID, cadence sql.NullString
	var amount, anchor sql.NullInt64
	var next, ruleCreated sql.NullTime
	if err := row.Scan(&g.ID, &g.CustomerID, &g.AccountID, &g.PocketID, &g.Name, &g.Currency, &g.TargetMicros,
		&g.TargetDate, &g.CreatedAt, &g.ReachedAt,
		&ruleID, &amount, &cadence, &anchor, &next, &ruleCreated); err != nil {
		return nil, err
	}
	if ruleID.Valid {
		g.Rule = &Rule{ID: ruleID.String, GoalID: g.ID, AmountMicros: amount.Int64, Cadence: Cadence(cadence.String),
			AnchorDay: int(anchor.Int64), NextRunOn: next.Time, CreatedAt: ruleCreated.Time}
	}
	return &g, nil
}

// Get devuelve una meta abierta del cliente, con lo juntado.
func (s *Service) Get(ctx context.Context, customerID, goalID string) (*Goal, error) {
	if _, err := uuid.Parse(goalID); err != nil {
		return nil, ErrNotFound
	}
	g, err := scanGoal(s.db.QueryRowContext(ctx, `SELECT `+goalColumns+goalFrom+`
		 WHERE g.id = $1 AND g.customer_id = $2 AND g.closed_at IS NULL`, goalID, customerID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if g.SavedMicros, err = s.pockets.Balance(ctx, customerID, g.PocketID); err != nil {
		return nil, err
	}
	return g, nil
}

// List devuelve las metas abiertas de una cuenta, de la más antigua a la más nueva.
func (s *Service) List(ctx context.Context, customerID, accountID string) ([]Goal, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+goalColumns+goalFrom+`
		 WHERE g.account_id = $1 AND g.customer_id = $2 AND g.closed_at IS NULL
		 ORDER BY g.created_at`, accountID, customerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Goal
	for rows.Next() {
		g, err := scanGoal(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *g)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}
	// Lo juntado sale de los bolsillos, en una sola consulta al core.
	list, err := s.pockets.List(ctx, customerID, accountID)
	if err != nil {
		return nil, err
	}
	balances := map[string]int64{}
	for _, p := range list {
		balances[p.ID] = p.BalanceMicros
	}
	for i := range out {
		out[i].SavedMicros = balances[out[i].PocketID]
	}
	return out, nil
}

// SetRule pone la regla de ahorro de una meta, o cambia la que tenía. El primer
// aporte toca hoy. Quien la llama ya hizo confirmar a la persona: `evidence` es
// con qué.
func (s *Service) SetRule(ctx context.Context, customerID, goalID string, amountMicros int64, cadence Cadence, evidence string) (*Rule, error) {
	if amountMicros <= 0 || (cadence != Weekly && cadence != Monthly) {
		return nil, ErrBadRule
	}
	goal, err := s.Get(ctx, customerID, goalID)
	if err != nil {
		return nil, err
	}
	if remaining := goal.TargetMicros - goal.SavedMicros; amountMicros > remaining {
		return nil, ErrRuleTooBig
	}
	today := s.today()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
		UPDATE goals.rules SET revoked_at = now(), revoked_reason = $2
		 WHERE goal_id = $1 AND revoked_at IS NULL`, goalID, EndedByHolder); err != nil {
		return nil, err
	}
	r := Rule{ID: uuid.NewString(), GoalID: goalID, AmountMicros: amountMicros, Cadence: cadence,
		AnchorDay: today.Day(), NextRunOn: today}
	err = tx.QueryRowContext(ctx, `
		INSERT INTO goals.rules (id, goal_id, customer_id, amount_micros, cadence, anchor_day, next_run_on, evidence)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING created_at`,
		r.ID, goalID, customerID, amountMicros, string(cadence), r.AnchorDay, today, evidence).Scan(&r.CreatedAt)
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && pqErr.Code == "23505" {
		// Otra regla nació a la vez para la misma meta.
		return nil, ErrBadRule
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &r, nil
}

// RevokeRule quita la regla de una meta. La meta y lo juntado siguen ahí.
func (s *Service) RevokeRule(ctx context.Context, customerID, goalID string) error {
	if _, err := s.Get(ctx, customerID, goalID); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE goals.rules SET revoked_at = now(), revoked_reason = $2
		 WHERE goal_id = $1 AND revoked_at IS NULL`, goalID, EndedByHolder)
	return err
}

// Close cierra la meta: quita su regla y devuelve lo juntado a la cuenta.
func (s *Service) Close(ctx context.Context, customerID, goalID string) error {
	goal, err := s.Get(ctx, customerID, goalID)
	if err != nil {
		return err
	}
	if err := s.end(ctx, goalID, EndedByClosed); err != nil {
		return err
	}
	if err := s.pockets.Close(ctx, customerID, goal.PocketID); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE goals.goals SET closed_at = now() WHERE id = $1`, goalID)
	return err
}

// PocketClosed cierra la meta de un bolsillo que se cerró por su cuenta.
func (s *Service) PocketClosed(ctx context.Context, pocketID string) error {
	var goalID string
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM goals.goals WHERE pocket_id = $1 AND closed_at IS NULL`, pocketID).Scan(&goalID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := s.end(ctx, goalID, EndedByClosed); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE goals.goals SET closed_at = now() WHERE id = $1`, goalID)
	return err
}

func (s *Service) end(ctx context.Context, goalID, reason string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE goals.rules SET revoked_at = now(), revoked_reason = $2
		 WHERE goal_id = $1 AND revoked_at IS NULL`, goalID, reason)
	return err
}

// Contributions devuelve lo que hizo la regla de la meta, del más reciente al
// más antiguo, también con reglas anteriores.
func (s *Service) Contributions(ctx context.Context, goalID string, limit int) ([]Contribution, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.period, c.run_on, c.outcome, c.asked_micros, c.amount_micros, c.reason, c.lowest_micros,
		       c.income_on, COALESCE(c.transaction_id::text, ''), c.created_at
		  FROM goals.contributions c JOIN goals.rules r ON r.id = c.rule_id
		 WHERE r.goal_id = $1
		 ORDER BY c.created_at DESC LIMIT $2`, goalID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Contribution
	for rows.Next() {
		var c Contribution
		var lowest sql.NullInt64
		if err := rows.Scan(&c.Period, &c.RunOn, &c.Outcome, &c.AskedMicros, &c.AmountMicros, &c.Reason,
			&lowest, &c.IncomeOn, &c.TransactionID, &c.CreatedAt); err != nil {
			return nil, err
		}
		if lowest.Valid {
			v := lowest.Int64
			c.LowestMicros = &v
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// RuleInfo es una regla viva, para el centro de permisos.
type RuleInfo struct {
	Rule
	GoalName  string
	AccountID string
	Currency  string
}

// Rules devuelve las reglas vivas del cliente.
func (s *Service) Rules(ctx context.Context, customerID string) ([]RuleInfo, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.id, r.goal_id, r.amount_micros, r.cadence, r.anchor_day, r.next_run_on, r.created_at,
		       g.name, g.account_id, g.currency
		  FROM goals.rules r JOIN goals.goals g ON g.id = r.goal_id
		 WHERE r.customer_id = $1 AND r.revoked_at IS NULL AND g.closed_at IS NULL
		 ORDER BY r.created_at`, customerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RuleInfo
	for rows.Next() {
		var ri RuleInfo
		var cadence string
		if err := rows.Scan(&ri.ID, &ri.GoalID, &ri.AmountMicros, &cadence, &ri.AnchorDay, &ri.NextRunOn,
			&ri.CreatedAt, &ri.GoalName, &ri.AccountID, &ri.Currency); err != nil {
			return nil, err
		}
		ri.Cadence = Cadence(cadence)
		out = append(out, ri)
	}
	return out, rows.Err()
}

// RuleName dice de qué meta es una regla, para nombrarla en el historial.
func (s *Service) RuleName(ctx context.Context, ruleID string) (string, error) {
	if _, err := uuid.Parse(ruleID); err != nil {
		return "", ErrNotFound
	}
	var name string
	err := s.db.QueryRowContext(ctx, `
		SELECT g.name FROM goals.rules r JOIN goals.goals g ON g.id = r.goal_id WHERE r.id = $1`,
		ruleID).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return name, err
}
