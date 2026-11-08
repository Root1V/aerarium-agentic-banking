package bff

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/aibank/aibank/services/control"
	"github.com/aibank/aibank/services/goals"
	"github.com/aibank/aibank/services/pockets"
)

// Las metas con ahorro automático autorizado (F3-03).
//
// Crear una meta abre su bolsillo y no pide nada: el dinero no se mueve. Ponerle
// una regla sí, porque desde ese momento algo mueve el dinero de la persona sin
// que ella esté: es darle un permiso, y se confirma con la cara como cualquier
// otro (nivel 4). Quitarla no pide nada; nunca hace falta probar quién es para
// quitarle poder a algo.

// WithGoals habilita las metas. Necesita los bolsillos.
func (s *Server) WithGoals(svc *goals.Service) *Server {
	s.goals = svc
	return s
}

func (s *Server) goalRoutes(mux *http.ServeMux) {
	mux.Handle("GET /v1/accounts/{accountID}/goals", s.requireAuth(s.handleListGoals))
	mux.Handle("POST /v1/accounts/{accountID}/goals", s.requireAuth(s.handleCreateGoal))
	mux.Handle("GET /v1/goals/{goalID}", s.requireAuth(s.handleGoal))
	mux.Handle("DELETE /v1/goals/{goalID}", s.requireAuth(s.handleCloseGoal))
	mux.Handle("PUT /v1/goals/{goalID}/rule", s.requireAuth(s.handleSetGoalRule))
	mux.Handle("DELETE /v1/goals/{goalID}/rule", s.requireAuth(s.handleRevokeGoalRule))
}

type goalRuleJSON struct {
	Amount  moneyJSON `json:"amount"`
	Cadence string    `json:"cadence"`
	// NextRunOn: el próximo aporte.
	NextRunOn string    `json:"next_run_on"`
	Since     time.Time `json:"since"`
}

type goalPaceJSON struct {
	Remaining moneyJSON `json:"remaining"`
	// ReachOn: cuándo se completa al ritmo de la regla.
	ReachOn *string `json:"reach_on,omitempty"`
	// OnTrack: llega antes de la fecha de la meta.
	OnTrack *bool `json:"on_track,omitempty"`
	// Suggested: lo que habría que apartar por aporte para llegar a la fecha.
	Suggested        *moneyJSON `json:"suggested,omitempty"`
	SuggestedCadence string     `json:"suggested_cadence"`
}

type contributionJSON struct {
	RunOn   string    `json:"run_on"`
	Outcome string    `json:"outcome"`
	Asked   moneyJSON `json:"asked"`
	Amount  moneyJSON `json:"amount"`
	// Reason: por qué apartó menos o nada (`keeps_cushion`,
	// `insufficient_funds`, `not_allowed`).
	Reason string `json:"reason,omitempty"`
	// Con qué se decidió: el punto más bajo previsto antes del ingreso, y cuándo
	// se esperaba el ingreso.
	Lowest        *moneyJSON `json:"lowest,omitempty"`
	IncomeOn      *string    `json:"income_on,omitempty"`
	TransactionID string     `json:"transaction_id,omitempty"`
}

type goalJSON struct {
	ID         string        `json:"id"`
	Name       string        `json:"name"`
	PocketID   string        `json:"pocket_id"`
	Target     moneyJSON     `json:"target"`
	Saved      moneyJSON     `json:"saved"`
	TargetDate *string       `json:"target_date,omitempty"`
	Reached    bool          `json:"reached"`
	Rule       *goalRuleJSON `json:"rule,omitempty"`
	Pace       goalPaceJSON  `json:"pace"`
	// Contributions, solo en el detalle: lo que hizo la regla, también lo que no.
	Contributions []contributionJSON `json:"contributions,omitempty"`
}

func dateString(t *time.Time) *string {
	if t == nil {
		return nil
	}
	v := t.Format(time.DateOnly)
	return &v
}

func (s *Server) toGoalJSON(g goals.Goal) goalJSON {
	money := func(v int64) moneyJSON { return moneyJSON{AmountMicros: v, Currency: g.Currency} }
	out := goalJSON{ID: g.ID, Name: g.Name, PocketID: g.PocketID, Target: money(g.TargetMicros),
		Saved: money(g.SavedMicros), TargetDate: dateString(g.TargetDate),
		Reached: g.SavedMicros >= g.TargetMicros}
	if g.Rule != nil {
		out.Rule = &goalRuleJSON{Amount: money(g.Rule.AmountMicros), Cadence: string(g.Rule.Cadence),
			NextRunOn: g.Rule.NextRunOn.Format(time.DateOnly), Since: g.Rule.CreatedAt}
	}
	zone, err := time.LoadLocation(s.timeZone)
	if err != nil {
		zone = time.UTC
	}
	now := time.Now().In(zone)
	pace := goals.PaceOf(g, time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC))
	out.Pace = goalPaceJSON{Remaining: money(pace.RemainingMicros), ReachOn: dateString(pace.ReachOn),
		OnTrack: pace.OnTrack, SuggestedCadence: string(pace.SuggestedCadence)}
	if pace.SuggestedMicros > 0 {
		v := money(pace.SuggestedMicros)
		out.Pace.Suggested = &v
	}
	return out
}

func (s *Server) handleListGoals(w http.ResponseWriter, r *http.Request, p *Principal) {
	accountID := r.PathValue("accountID")
	if _, err := s.authorizeAccount(r.Context(), p, accountID); err != nil {
		s.writeCoreError(w, r, err)
		return
	}
	list, err := s.goals.List(r.Context(), p.CustomerID, accountID)
	if err != nil {
		s.goalError(w, r, err)
		return
	}
	out := []goalJSON{}
	for _, g := range list {
		out = append(out, s.toGoalJSON(g))
	}
	writeJSON(w, http.StatusOK, map[string]any{"goals": out})
}

func (s *Server) handleCreateGoal(w http.ResponseWriter, r *http.Request, p *Principal) {
	accountID := r.PathValue("accountID")
	if _, err := s.authorizeAccount(r.Context(), p, accountID); err != nil {
		s.writeCoreError(w, r, err)
		return
	}
	var body struct {
		Name         string `json:"name"`
		TargetMicros int64  `json:"target_micros"`
		TargetDate   string `json:"target_date"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	var date *time.Time
	if body.TargetDate != "" {
		d, err := time.Parse(time.DateOnly, body.TargetDate)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "target_date es AAAA-MM-DD")
			return
		}
		date = &d
	}
	goal, err := s.goals.Create(r.Context(), p.CustomerID, accountID, body.Name, body.TargetMicros, date)
	if err != nil {
		s.goalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, s.toGoalJSON(*goal))
}

// goalContributionsLimit: el historial de una meta, sin paginar todavía.
const goalContributionsLimit = 52

func (s *Server) handleGoal(w http.ResponseWriter, r *http.Request, p *Principal) {
	goal, err := s.goals.Get(r.Context(), p.CustomerID, r.PathValue("goalID"))
	if err != nil {
		s.goalError(w, r, err)
		return
	}
	out := s.toGoalJSON(*goal)
	history, err := s.goals.Contributions(r.Context(), goal.ID, goalContributionsLimit)
	if err != nil {
		s.goalError(w, r, err)
		return
	}
	money := func(v int64) moneyJSON { return moneyJSON{AmountMicros: v, Currency: goal.Currency} }
	out.Contributions = []contributionJSON{}
	for _, c := range history {
		cj := contributionJSON{RunOn: c.RunOn.Format(time.DateOnly), Outcome: c.Outcome,
			Asked: money(c.AskedMicros), Amount: money(c.AmountMicros), Reason: c.Reason,
			IncomeOn: dateString(c.IncomeOn), TransactionID: c.TransactionID}
		if c.LowestMicros != nil {
			v := money(*c.LowestMicros)
			cj.Lowest = &v
		}
		out.Contributions = append(out.Contributions, cj)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCloseGoal(w http.ResponseWriter, r *http.Request, p *Principal) {
	if err := s.goals.Close(r.Context(), p.CustomerID, r.PathValue("goalID")); err != nil {
		s.goalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSetGoalRule(w http.ResponseWriter, r *http.Request, p *Principal) {
	ctx := r.Context()
	goal, err := s.goals.Get(ctx, p.CustomerID, r.PathValue("goalID"))
	if err != nil {
		s.goalError(w, r, err)
		return
	}
	var body struct {
		AmountMicros int64                  `json:"amount_micros"`
		Cadence      string                 `json:"cadence"`
		Confirmation *operationConfirmation `json:"confirmation"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	cadence := goals.Cadence(body.Cadence)
	if body.AmountMicros <= 0 || (cadence != goals.Weekly && cadence != goals.Monthly) {
		writeError(w, http.StatusBadRequest, "invalid_request", goals.ErrBadRule.Error())
		return
	}
	if body.AmountMicros > goal.TargetMicros-goal.SavedMicros {
		writeError(w, http.StatusUnprocessableEntity, "rule_too_big", goals.ErrRuleTooBig.Error())
		return
	}

	// Lo que firma el teléfono lleva la meta, el monto y la cadencia: la firma
	// de una regla de 10 por mes no sirve para una de 100 por semana.
	auth, ok := s.authorizeOperation(w, r, p, holderOperation{
		action:       control.ActionGrantPermission,
		counterparty: goal.PocketID,
		canonical: []byte("grant_permission" +
			"\nkind=savings_rule" +
			"\ngoal=" + goal.ID +
			"\naccount=" + goal.AccountID +
			"\namount=" + strconv.FormatInt(body.AmountMicros, 10) +
			"\ncurrency=" + goal.Currency +
			"\ncadence=" + body.Cadence),
		summary: confirmationSummary{Kind: "savings_rule", AmountMicros: body.AmountMicros,
			Currency: goal.Currency, ToName: goal.Name, Cadence: body.Cadence},
	}, body.Confirmation)
	if !ok {
		return
	}
	rule, err := s.goals.SetRule(ctx, p.CustomerID, goal.ID, body.AmountMicros, cadence, auth.Evidence)
	if err != nil {
		s.goalError(w, r, err)
		return
	}
	// El primer aporte toca hoy: la persona ve en el momento qué hace su regla.
	// Si falla, lo hará la próxima vuelta.
	if _, err := s.goals.RunRule(ctx, rule.ID); err != nil {
		s.logger.WarnContext(ctx, "primer aporte de la regla", "error", err)
	}
	goal, err = s.goals.Get(ctx, p.CustomerID, goal.ID)
	if err != nil {
		s.goalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.toGoalJSON(*goal))
}

func (s *Server) handleRevokeGoalRule(w http.ResponseWriter, r *http.Request, p *Principal) {
	if err := s.goals.RevokeRule(r.Context(), p.CustomerID, r.PathValue("goalID")); err != nil {
		s.goalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) goalError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, goals.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, goals.ErrBadName), errors.Is(err, goals.ErrBadTarget), errors.Is(err, goals.ErrBadDate),
		errors.Is(err, goals.ErrBadRule):
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.Is(err, goals.ErrRuleTooBig):
		writeError(w, http.StatusUnprocessableEntity, "rule_too_big", err.Error())
	case errors.Is(err, pockets.ErrTooMany), errors.Is(err, pockets.ErrNotAnAccount):
		writeError(w, http.StatusConflict, "pocket_conflict", err.Error())
	default:
		s.pocketError(w, r, err)
	}
}
