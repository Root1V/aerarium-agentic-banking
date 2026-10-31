// Package control decide, en un solo sitio, qué puede hacer cada actor con el
// dinero de un cliente (doc 15, F3-04).
//
// Hasta ahora cada pieza traía su propia regla: los mandatos ponían topes a los
// agentes, la confirmación reforzada protegía las transferencias, y conceder un
// permiso no pedía nada. Con más actores por venir —reglas de ahorro, un
// asistente— cada feature acabaría inventando sus permisos. Aquí se fijan una vez:
//
//   - Cada acción tiene un NIVEL de autonomía: 1 observar, 2 recomendar,
//     3 ejecutar bajo mandato, 4 alto impacto.
//   - La persona puede hacerlo todo, pero lo de nivel 4 lo confirma siempre con
//     su teléfono, y las transferencias siguen la política de stepup.
//   - Cualquier otro actor necesita un permiso que cubra la acción, con nivel
//     suficiente y vigente. El nivel 4 no se delega nunca: siempre es humano.
//
// Cada decisión queda en un registro de solo agregar. Si no se puede registrar,
// la operación no sigue: una decisión sin rastro es la que no se puede explicar.
package control

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"slices"
	"time"

	"github.com/aibank/aibank/services/stepup"
)

//go:embed schema.sql
var schema string

// Level es cuánta autonomía exige una acción.
type Level int

const (
	// LevelObserve: leer. Saldos, movimientos.
	LevelObserve Level = 1
	// LevelRecommend: preparar algo para que la persona lo apruebe.
	LevelRecommend Level = 2
	// LevelMandate: ejecutar dentro de un permiso con topes.
	LevelMandate Level = 3
	// LevelHuman: alto impacto. Lo decide la persona, con su teléfono.
	LevelHuman Level = 4
)

// Action es lo que se quiere hacer.
type Action string

const (
	// ActionTransfer: enviar dinero a otra cuenta.
	ActionTransfer Action = "transfer"
	// ActionMandatePayment: cobrar dentro de un permiso de pago (modelo B).
	ActionMandatePayment Action = "mandate_payment"
	// ActionGrantPermission: darle a otro actor poder sobre el dinero.
	ActionGrantPermission Action = "grant_permission"
)

// catalog es el nivel de cada acción. Lo que no está aquí se niega.
var catalog = map[Action]Level{
	ActionTransfer:        LevelMandate,
	ActionMandatePayment:  LevelMandate,
	ActionGrantPermission: LevelHuman,
}

// LevelOf devuelve el nivel de una acción, o 0 si no está en el catálogo.
func LevelOf(a Action) Level { return catalog[a] }

// ActorKind es quién pide la operación.
type ActorKind string

const (
	ActorHolder    ActorKind = "holder"
	ActorAgent     ActorKind = "agent"
	ActorRule      ActorKind = "rule"
	ActorAssistant ActorKind = "assistant"
)

type Actor struct {
	Kind ActorKind
	// ID: el teléfono de la persona, la plataforma del agente, la regla.
	ID string
}

// Grant es el permiso con el que actúa quien no es la persona.
type Grant struct {
	// Ref identifica el permiso (un mandato, una regla) para el registro.
	Ref     string
	Level   Level
	Actions []Action
	// MaxPerOperation en micras; nil es sin tope por operación.
	MaxPerOperation *int64
	// Remaining es lo que queda del tope total, en micras; nil es sin tope total.
	// Es una foto: quien lleva la cuenta de verdad (el core, para los mandatos) la
	// vuelve a comprobar al mover el dinero.
	Remaining *int64
	ExpiresAt time.Time
	Revoked   bool
}

// Request es una operación por decidir.
type Request struct {
	CustomerID string
	Actor      Actor
	Action     Action
	// AmountMicros y Counterparty, cuando la acción mueve dinero.
	AmountMicros int64
	Counterparty string
	// Operation es la descripción canónica: la que firma el teléfono.
	Operation []byte
	// Grant, para actores que no son la persona.
	Grant *Grant
}

// Outcome es lo que se decidió.
type Outcome string

const (
	Allow   Outcome = "allow"
	Confirm Outcome = "confirm"
	Deny    Outcome = "deny"
)

// Reason explica la decisión. Vacía cuando simplemente se permite.
type Reason string

const (
	ReasonNone Reason = ""
	// Pide confirmación.
	ReasonNewPayee   Reason = "new_payee"
	ReasonAmount     Reason = "amount"
	ReasonHighImpact Reason = "high_impact"
	// Se niega.
	ReasonUnknownAction Reason = "unknown_action"
	ReasonHumanOnly     Reason = "human_only"
	ReasonNoGrant       Reason = "no_grant"
	ReasonGrantInactive Reason = "grant_inactive"
	ReasonOutOfScope    Reason = "out_of_scope"
	ReasonLevelTooLow   Reason = "level_too_low"
	ReasonOverLimit     Reason = "over_limit"
)

type Decision struct {
	Outcome Outcome
	Reason  Reason
	Level   Level
}

// TransferPolicy es la política de confirmación de transferencias (stepup).
type TransferPolicy interface {
	Required(ctx context.Context, t stepup.Transfer) (stepup.Reason, error)
}

type Service struct {
	db        *sql.DB
	transfers TransferPolicy
}

func NewService(db *sql.DB, transfers TransferPolicy) *Service {
	return &Service{db: db, transfers: transfers}
}

func (s *Service) Migrate(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// Dos servicios que arrancan a la vez no deben pisarse al crear el disparador.
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('control.schema'))`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, schema); err != nil {
		return err
	}
	return tx.Commit()
}

// Decide resuelve la operación y deja constancia. Si no puede dejarla, devuelve
// el error y la operación no debe seguir.
func (s *Service) Decide(ctx context.Context, req Request) (Decision, error) {
	d, err := s.evaluate(ctx, req)
	if err != nil {
		return Decision{}, err
	}
	evidence := ""
	if req.Grant != nil {
		evidence = "grant:" + req.Grant.Ref
	}
	if err := s.record(ctx, req, d.Level, string(d.Outcome), d.Reason, evidence); err != nil {
		return Decision{}, err
	}
	return d, nil
}

// Confirmed anota el resultado de una confirmación pedida por Decide: `ok` si el
// teléfono firmó esa operación, y con qué (`evidence`: teléfono y método).
func (s *Service) Confirmed(ctx context.Context, req Request, ok bool, evidence string) error {
	outcome := "rejected"
	if ok {
		outcome = "confirmed"
	}
	return s.record(ctx, req, LevelOf(req.Action), outcome, ReasonNone, evidence)
}

func (s *Service) evaluate(ctx context.Context, req Request) (Decision, error) {
	level, known := catalog[req.Action]
	if !known {
		return Decision{Outcome: Deny, Reason: ReasonUnknownAction}, nil
	}
	if req.Actor.Kind == ActorHolder {
		return s.forHolder(ctx, req, level)
	}
	return s.forDelegate(req, level), nil
}

// forHolder: la persona puede todo, pero confirma lo que es de riesgo.
func (s *Service) forHolder(ctx context.Context, req Request, level Level) (Decision, error) {
	if level == LevelHuman {
		return Decision{Outcome: Confirm, Reason: ReasonHighImpact, Level: level}, nil
	}
	if req.Action == ActionTransfer && s.transfers != nil {
		reason, err := s.transfers.Required(ctx, stepup.Transfer{
			CustomerID:   req.CustomerID,
			ToAccountID:  req.Counterparty,
			AmountMicros: req.AmountMicros,
		})
		if err != nil {
			return Decision{}, fmt.Errorf("política de transferencias: %w", err)
		}
		if reason != stepup.ReasonNone {
			return Decision{Outcome: Confirm, Reason: Reason(reason), Level: level}, nil
		}
	}
	return Decision{Outcome: Allow, Level: level}, nil
}

// forDelegate: quien no es la persona solo hace lo que un permiso vigente le
// deja, y nunca lo de nivel 4.
func (s *Service) forDelegate(req Request, level Level) Decision {
	deny := func(r Reason) Decision { return Decision{Outcome: Deny, Reason: r, Level: level} }
	if level == LevelHuman {
		return deny(ReasonHumanOnly)
	}
	g := req.Grant
	switch {
	case g == nil:
		return deny(ReasonNoGrant)
	case g.Revoked || !time.Now().Before(g.ExpiresAt):
		return deny(ReasonGrantInactive)
	case !slices.Contains(g.Actions, req.Action):
		return deny(ReasonOutOfScope)
	case g.Level < level:
		return deny(ReasonLevelTooLow)
	case g.MaxPerOperation != nil && req.AmountMicros > *g.MaxPerOperation,
		g.Remaining != nil && req.AmountMicros > *g.Remaining:
		return deny(ReasonOverLimit)
	}
	return Decision{Outcome: Allow, Level: level}
}

func (s *Service) record(ctx context.Context, req Request, level Level, outcome string, reason Reason, evidence string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO control.decisions
			(customer_id, actor_kind, actor_id, action, level, outcome, reason, operation, evidence)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		req.CustomerID, string(req.Actor.Kind), req.Actor.ID, string(req.Action), int(level),
		outcome, string(reason), string(req.Operation), evidence)
	if err != nil {
		return fmt.Errorf("registrar decisión: %w", err)
	}
	return nil
}

// Entry es una decisión registrada.
type Entry struct {
	DecidedAt time.Time
	Actor     Actor
	Action    Action
	Level     Level
	Outcome   string
	Reason    Reason
	Operation string
	Evidence  string
}

// History devuelve las decisiones sobre un cliente, de la más reciente a la más
// antigua.
func (s *Service) History(ctx context.Context, customerID string, limit int) ([]Entry, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT decided_at, actor_kind, actor_id, action, level, outcome, reason, operation, evidence
		  FROM control.decisions WHERE customer_id = $1
		 ORDER BY decided_at DESC, id DESC LIMIT $2`, customerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		var kind, action, reason string
		var level int
		if err := rows.Scan(&e.DecidedAt, &kind, &e.Actor.ID, &action, &level, &e.Outcome, &reason,
			&e.Operation, &e.Evidence); err != nil {
			return nil, err
		}
		e.Actor.Kind, e.Action, e.Level, e.Reason = ActorKind(kind), Action(action), Level(level), Reason(reason)
		out = append(out, e)
	}
	return out, rows.Err()
}
