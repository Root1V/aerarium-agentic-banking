package bff

import (
	"net/http"
	"time"

	"github.com/aibank/aibank/services/forecast"
)

// La previsión de saldo hasta el próximo ingreso (F3-01). Solo lee y recomienda:
// nada de lo que propone se ejecuta desde aquí.

// WithForecast habilita la previsión de saldo.
func (s *Server) WithForecast(svc *forecast.Service) *Server {
	s.forecast = svc
	return s
}

type pointJSON struct {
	On     string    `json:"on"`
	Amount moneyJSON `json:"amount"`
}

type forecastIncomeJSON struct {
	Amount     moneyJSON `json:"amount"`
	ExpectedOn string    `json:"expected_on"`
	Cadence    string    `json:"cadence"`
	// Synthetic: es el sueldo de prueba del sandbox. La app lo dice.
	Synthetic bool `json:"synthetic,omitempty"`
}

type forecastCardJSON struct {
	DueOn   string    `json:"due_on"`
	Minimum moneyJSON `json:"minimum"`
	Total   moneyJSON `json:"total"`
	Overdue bool      `json:"overdue,omitempty"`
	// Reserved: lo apartado en el bolsillo para la tarjeta.
	Reserved *moneyJSON `json:"reserved,omitempty"`
}

type forecastOptionJSON struct {
	Kind   string     `json:"kind"`
	Amount moneyJSON  `json:"amount"`
	Rest   *moneyJSON `json:"rest,omitempty"`
}

type forecastJSON struct {
	Today      string               `json:"today"`
	Until      string               `json:"until"`
	Balance    moneyJSON            `json:"balance"`
	DailySpend moneyJSON            `json:"daily_spend"`
	LowHistory bool                 `json:"low_history,omitempty"`
	Income     *forecastIncomeJSON  `json:"income,omitempty"`
	Card       *forecastCardJSON    `json:"card,omitempty"`
	Lowest     pointJSON            `json:"lowest"`
	AfterCard  *pointJSON           `json:"after_card,omitempty"`
	Status     string               `json:"status"`
	Cause      string               `json:"cause,omitempty"`
	Options    []forecastOptionJSON `json:"options"`
}

func (s *Server) handleForecast(w http.ResponseWriter, r *http.Request, p *Principal) {
	accountID := r.PathValue("accountID")
	if _, err := s.authorizeAccount(r.Context(), p, accountID); err != nil {
		s.writeCoreError(w, r, err)
		return
	}
	f, err := s.forecast.Forecast(r.Context(), p.CustomerID, accountID)
	if err != nil {
		// Sin previsión la app sigue funcionando: es un aviso, no una operación.
		s.logger.WarnContext(r.Context(), "previsión de saldo", "error", err)
		writeError(w, http.StatusServiceUnavailable, "unavailable", "no pudimos calcular la previsión")
		return
	}
	writeJSON(w, http.StatusOK, toForecastJSON(f))
}

func toForecastJSON(f *forecast.Forecast) forecastJSON {
	money := func(micros int64) moneyJSON { return moneyJSON{AmountMicros: micros, Currency: f.Currency} }
	date := func(t time.Time) string { return t.Format(time.DateOnly) }
	out := forecastJSON{
		Today: date(f.Today), Until: date(f.Until),
		Balance: money(f.BalanceMicros), DailySpend: money(f.DailySpendMicros), LowHistory: f.LowHistory,
		Lowest: pointJSON{On: date(f.Lowest.On), Amount: money(f.Lowest.AmountMicros)},
		Status: f.Status, Cause: f.Cause, Options: []forecastOptionJSON{},
	}
	if f.Income != nil {
		out.Income = &forecastIncomeJSON{Amount: money(f.Income.AmountMicros), ExpectedOn: date(f.Income.ExpectedOn),
			Cadence: f.Income.Cadence, Synthetic: f.Income.Synthetic}
	}
	if f.Card != nil {
		out.Card = &forecastCardJSON{DueOn: date(f.Card.DueOn), Minimum: money(f.Card.MinimumMicros),
			Total: money(f.Card.TotalMicros), Overdue: f.Card.Overdue}
		if f.Card.ReservedMicros > 0 {
			reserved := money(f.Card.ReservedMicros)
			out.Card.Reserved = &reserved
		}
	}
	if f.AfterCard != nil {
		out.AfterCard = &pointJSON{On: date(f.AfterCard.On), Amount: money(f.AfterCard.AmountMicros)}
	}
	for _, o := range f.Options {
		opt := forecastOptionJSON{Kind: o.Kind, Amount: money(o.AmountMicros)}
		if o.Kind == "pay_minimum" {
			rest := money(o.RestMicros)
			opt.Rest = &rest
		}
		out.Options = append(out.Options, opt)
	}
	return out
}
