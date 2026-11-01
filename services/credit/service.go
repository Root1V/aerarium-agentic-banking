// Package credit origina y opera tarjetas de crédito.
//
// Originar es: evaluar al cliente con el motor de riesgo sobre lo que el banco
// observó en su cuenta, archivar el expediente de la decisión, y —si se aprobó—
// abrir en el core la línea con el límite otorgado. Este paquete no decide nada
// de riesgo; aplica la decisión y la deja trazable.
//
// Usar la línea (disposición) y pagarla son asientos del core, que hace cumplir
// el límite, el bloqueo por mora y el no pagar de más.
package credit

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/aibank/aibank/clients/go/coreclient"
	corev1 "github.com/aibank/aibank/clients/go/corev1"
	"github.com/google/uuid"
)

//go:embed schema.sql
var schema string

var (
	// ErrAlreadyHasLine: el cliente ya tiene una tarjeta viva.
	ErrAlreadyHasLine = errors.New("el cliente ya tiene una línea de crédito")
	// ErrNoLine: el cliente no tiene tarjeta.
	ErrNoLine = errors.New("el cliente no tiene línea de crédito")
	// ErrNoStatement: la tarjeta todavía no tiene estado de cuenta.
	ErrNoStatement = errors.New("la tarjeta todavía no tiene estado de cuenta")
	// ErrRiskUnavailable: el motor de riesgo no respondió. Sin él no se decide.
	ErrRiskUnavailable = errors.New("motor de riesgo no disponible")
)

// Outcomes del motor.
const (
	OutcomeApproved         = "approved"
	OutcomeNeedsMoreHistory = "needs_more_history"
	OutcomeDeclined         = "declined"
)

type Config struct {
	// AnnualRateBps es la tasa efectiva anual de las líneas nuevas, en puntos
	// básicos. PROVISIONAL: la fija el negocio dentro del tope del BCRP.
	AnnualRateBps int32
	// PaymentDays: días desde el corte hasta el vencimiento del pago.
	PaymentDays int32
	// ObservedDays: cuánto historial se le pasa al motor.
	ObservedDays int
	// Cooldown: tras una solicitud no aprobada, cuánto esperar para que una
	// nueva se evalúe de verdad. Antes, se devuelve la misma decisión: evaluar a
	// alguien diez veces seguidas no cambia lo observado.
	Cooldown time.Duration
	// SyntheticHistory, SOLO en el sandbox: se le suman al motor seis meses de un
	// ingreso de prueba, porque una cuenta recién abierta nunca califica. Queda
	// registrado en la decisión.
	SyntheticHistory bool
}

func (c *Config) defaults() {
	if c.AnnualRateBps == 0 {
		c.AnnualRateBps = 4500
	}
	if c.PaymentDays == 0 {
		c.PaymentDays = 20
	}
	if c.ObservedDays == 0 {
		c.ObservedDays = 180
	}
	if c.Cooldown == 0 {
		c.Cooldown = 30 * 24 * time.Hour
	}
}

type Service struct {
	db   *sql.DB
	core *coreclient.Client
	risk Assessor
	cfg  Config
	now  func() time.Time
}

func NewService(db *sql.DB, core *coreclient.Client, risk Assessor, cfg Config) *Service {
	cfg.defaults()
	return &Service{db: db, core: core, risk: risk, cfg: cfg, now: time.Now}
}

func (s *Service) Migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, schema)
	return err
}

// Decision es lo que se le comunica a la persona.
type Decision struct {
	ApplicationID    string
	Outcome          string
	LimitMicros      int64
	Currency         string
	Reasons          []string
	SyntheticHistory bool
	// Line, si se aprobó.
	Line *corev1.CreditLine
}

// Apply evalúa al cliente sobre su cuenta y, si se aprueba, abre su tarjeta.
func (s *Service) Apply(ctx context.Context, customerID, accountID, holderName string) (*Decision, error) {
	if _, err := s.core.GetCustomerCreditLine(ctx, customerID); err == nil {
		return nil, ErrAlreadyHasLine
	} else if !errors.Is(err, coreclient.ErrNotFound) {
		return nil, err
	}
	if recent, err := s.recentDecision(ctx, customerID); err != nil || recent != nil {
		return recent, err
	}

	now := s.now().UTC()
	real, synthetic, err := s.Observed(ctx, accountID)
	if err != nil {
		return nil, err
	}
	movements := append(real, synthetic...)
	assessment, err := s.risk.Assess(ctx, AssessmentRequest{
		CustomerID: customerID, AsOf: now.Format(time.DateOnly), EvaluatedAt: now,
		Currency: "USD", ObservedDays: s.cfg.ObservedDays, Movements: movements,
	})
	if err != nil {
		return nil, err
	}

	d := &Decision{
		ApplicationID: uuid.NewString(), Outcome: assessment.Outcome, LimitMicros: assessment.LimitMicros,
		Currency: assessment.Currency, Reasons: assessment.Reasons, SyntheticHistory: s.cfg.SyntheticHistory,
	}
	reasons, _ := json.Marshal(nonNil(d.Reasons))
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO credit.applications (id, customer_id, account_id, outcome, limit_micros, currency,
		                                 score, model_version, reasons, record, synthetic_history)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		d.ApplicationID, customerID, accountID, d.Outcome, d.LimitMicros, d.Currency,
		assessment.Score, assessment.ModelVersion, reasons, []byte(assessment.Record), d.SyntheticHistory); err != nil {
		return nil, fmt.Errorf("archivar la decisión: %w", err)
	}
	if d.Outcome != OutcomeApproved {
		return d, nil
	}

	// El corte cae el mismo día del mes en que se abrió, hasta el 28: todos los
	// meses tienen ese día.
	cycleDay := int32(min(now.Day(), 28))
	line, err := s.core.OpenCreditLine(ctx, coreclient.NewCreditLine{
		CustomerID: customerID, Name: holderName, Currency: d.Currency, LimitMicros: d.LimitMicros,
		AnnualRateBps: s.cfg.AnnualRateBps, CycleDay: cycleDay, PaymentDays: s.cfg.PaymentDays,
		DecisionRef: d.ApplicationID,
	})
	if errors.Is(err, coreclient.ErrAlreadyExists) {
		// Dos solicitudes a la vez: la otra abrió la línea primero.
		return nil, ErrAlreadyHasLine
	}
	if err != nil {
		return nil, err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE credit.applications SET credit_line_id = $2 WHERE id = $1`,
		d.ApplicationID, line.Id); err != nil {
		return nil, err
	}
	d.Line = line
	return d, nil
}

// recentDecision devuelve la última decisión no aprobada si es reciente.
func (s *Service) recentDecision(ctx context.Context, customerID string) (*Decision, error) {
	var (
		d       Decision
		reasons []byte
		created time.Time
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT id, outcome, limit_micros, currency, reasons, synthetic_history, created_at
		  FROM credit.applications WHERE customer_id = $1 AND outcome <> 'approved'
		 ORDER BY created_at DESC LIMIT 1`, customerID).
		Scan(&d.ApplicationID, &d.Outcome, &d.LimitMicros, &d.Currency, &reasons, &d.SyntheticHistory, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if s.now().Sub(created) >= s.cfg.Cooldown {
		return nil, nil
	}
	_ = json.Unmarshal(reasons, &d.Reasons)
	return &d, nil
}

// Observed es lo que el motor ve de la cuenta: los movimientos reales de la
// ventana de evaluación y, en el sandbox, el historial sintético que se usó para
// la tarjeta, aparte. La previsión de saldo lee de aquí para que el sueldo que ve
// sea el mismo con el que se aprobó la tarjeta, sin confundir el gasto inventado
// con el de la persona.
func (s *Service) Observed(ctx context.Context, accountID string) (real, synthetic []RiskMovement, err error) {
	now := s.now().UTC()
	real, err = s.movements(ctx, accountID, now)
	if err != nil {
		return nil, nil, err
	}
	if s.cfg.SyntheticHistory {
		synthetic = syntheticHistory(now)
	}
	return real, synthetic, nil
}

// movements trae lo observado en la cuenta durante la ventana de evaluación.
func (s *Service) movements(ctx context.Context, accountID string, now time.Time) ([]RiskMovement, error) {
	since := now.AddDate(0, 0, -s.cfg.ObservedDays)
	var out []RiskMovement
	cursor := ""
	for page := 0; page < 20; page++ {
		statement, err := s.core.ListMovements(ctx, accountID, 100, cursor)
		if err != nil {
			return nil, err
		}
		for _, m := range statement.Movements {
			if m.PostedAt.Before(since) {
				return out, nil
			}
			if m.Kind == coreclient.PocketTransferKind {
				// Apartar en un bolsillo y devolverlo no es gastar ni cobrar: si
				// contara, mover dinero de ida y vuelta inflaría el ingreso que ve
				// el motor, y con él el límite de la tarjeta.
				continue
			}
			direction := "out"
			if m.Direction == corev1.Direction_DIRECTION_CREDIT {
				direction = "in"
			}
			counterparty := m.CounterpartyAccountID
			if counterparty == "" {
				counterparty = m.Kind
			}
			out = append(out, RiskMovement{
				OccurredAt: m.PostedAt, Direction: direction, AmountMicros: m.AmountMicros,
				Counterparty: counterparty, Kind: m.Kind,
			})
		}
		if statement.NextCursor == "" {
			break
		}
		cursor = statement.NextCursor
	}
	return out, nil
}

// syntheticHistory: seis meses de un sueldo de prueba de 1.500 y gastos en
// varios comercios. Solo para el sandbox.
func syntheticHistory(now time.Time) []RiskMovement {
	var out []RiskMovement
	for m := 0; m < 6; m++ {
		payday := now.AddDate(0, -m, -3)
		// Como sueldo (`payroll`): es lo que simula, y así el motor lo reconoce
		// como ingreso recurrente, igual que haría con uno de verdad.
		out = append(out, RiskMovement{OccurredAt: payday, Direction: "in", AmountMicros: 1_500_000_000,
			Counterparty: "sandbox-empleador", Kind: "payroll"})
		for i := 0; i < 4; i++ {
			out = append(out, RiskMovement{OccurredAt: payday.AddDate(0, 0, 3+i*6), Direction: "out",
				AmountMicros: 150_000_000, Counterparty: fmt.Sprintf("sandbox-comercio-%d", i), Kind: "sandbox_history"})
		}
	}
	return out
}

// Line devuelve la tarjeta del cliente.
func (s *Service) Line(ctx context.Context, customerID string) (*corev1.CreditLine, error) {
	line, err := s.core.GetCustomerCreditLine(ctx, customerID)
	if errors.Is(err, coreclient.ErrNotFound) {
		return nil, ErrNoLine
	}
	return line, err
}

// Draw pasa dinero de la tarjeta a una cuenta del cliente: la deuda sube.
func (s *Service) Draw(ctx context.Context, customerID, toAccountID string, amountMicros int64, key string) (*coreclient.PostResult, error) {
	line, err := s.Line(ctx, customerID)
	if err != nil {
		return nil, err
	}
	currency := line.Limit.Currency
	return s.core.Post(ctx, "credit-draw:"+customerID+":"+key, "credit_drawdown", []coreclient.Entry{
		coreclient.Debit(line.AccountId, amountMicros, currency),
		coreclient.Credit(toAccountID, amountMicros, currency),
	}, "disposición de la tarjeta de crédito")
}

// Pay paga la tarjeta desde una cuenta del cliente: la deuda baja.
//
// Tras el pago se revisa la mora en el momento: quien estaba bloqueado y se puso
// al día tiene que poder usar la tarjeta ya, no cuando pase el barrendero.
func (s *Service) Pay(ctx context.Context, customerID, fromAccountID string, amountMicros int64, key string) (*coreclient.PostResult, error) {
	line, err := s.Line(ctx, customerID)
	if err != nil {
		return nil, err
	}
	currency := line.Limit.Currency
	result, err := s.core.Post(ctx, "credit-pay:"+customerID+":"+key, "credit_payment", []coreclient.Entry{
		coreclient.Debit(fromAccountID, amountMicros, currency),
		coreclient.Credit(line.AccountId, amountMicros, currency),
	}, "pago de la tarjeta de crédito")
	if err != nil {
		return nil, err
	}
	// Si la revisión falla, el pago ya está hecho: lo desbloqueará el barrendero.
	_, _ = s.core.ReviewCreditLine(ctx, line.Id, nil)
	return result, nil
}

// Statements devuelve los estados de cuenta de la tarjeta, del más reciente al
// más antiguo.
func (s *Service) Statements(ctx context.Context, customerID string, limit int32) ([]*corev1.CreditStatement, error) {
	line, err := s.Line(ctx, customerID)
	if err != nil {
		return nil, err
	}
	return s.core.ListStatements(ctx, line.Id, limit)
}

// CloseCycleNow corta el estado de cuenta ahora. SOLO para el sandbox: en
// producción el corte lo hace el barrendero en el día de corte.
func (s *Service) CloseCycleNow(ctx context.Context, customerID string) (*corev1.CreditStatement, error) {
	line, err := s.Line(ctx, customerID)
	if err != nil {
		return nil, err
	}
	return s.core.CloseCycle(ctx, line.Id, nil)
}

// PassDueDate revisa la mora como si ya hubiera pasado el vencimiento del último
// estado de cuenta. SOLO para el sandbox: nadie puede esperar veinte días para
// ver qué pasa si no paga.
func (s *Service) PassDueDate(ctx context.Context, customerID string) (*corev1.CreditLine, error) {
	line, err := s.Line(ctx, customerID)
	if err != nil {
		return nil, err
	}
	statements, err := s.core.ListStatements(ctx, line.Id, 1)
	if err != nil {
		return nil, err
	}
	if len(statements) == 0 {
		return nil, ErrNoStatement
	}
	due, err := time.Parse(time.DateOnly, statements[0].DueDate)
	if err != nil {
		return nil, err
	}
	asOf := due.AddDate(0, 0, 1).Add(12 * time.Hour)
	return s.core.ReviewCreditLine(ctx, line.Id, &asOf)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
