// Package forecast proyecta el saldo de una cuenta hasta el próximo ingreso y
// avisa si no alcanza (doc 15, F3-01).
//
// «Tu tarjeta vence el 16 y te dejaría con −40 antes de tu sueldo del 30» es una
// frase que se puede decir con lo que el banco ya observa: el saldo de hoy, lo que
// la persona suele gastar por día, el próximo ingreso que el motor de riesgo
// espera y el pago de la tarjeta. No hay modelo opaco: cada número de la
// previsión se puede explicar.
//
// Es nivel 1–2 de la capa de control: observa y recomienda. Nada de lo que propone
// se ejecuta solo.
package forecast

import "time"

// SpendWindow es cuánto historial se mira para estimar el gasto diario.
const SpendWindow = 60

// MinHistoryDays: con menos días que estos, el gasto diario es una estimación
// débil, y la previsión lo dice.
const MinHistoryDays = 14

// DefaultHorizon: sin un ingreso que se pueda fechar, se proyecta este plazo.
const DefaultHorizon = 30

// TightDays: si en el punto más bajo queda menos de lo que se gasta en estos días,
// se llega, pero justo.
const TightDays = 5

// Movement es un movimiento de la cuenta, desde el punto de vista del titular.
type Movement struct {
	At           time.Time
	Out          bool
	AmountMicros int64
	Kind         string
}

// Income es el próximo ingreso esperado.
type Income struct {
	AmountMicros int64
	ExpectedOn   time.Time
	Cadence      string
	// Synthetic: es el sueldo de prueba del sandbox, no uno observado.
	Synthetic bool
}

// CardDue es lo que queda por pagar del último estado de cuenta de la tarjeta.
type CardDue struct {
	DueOn         time.Time
	MinimumMicros int64
	TotalMicros   int64
	// Overdue: el vencimiento ya pasó y no se pagó el mínimo.
	Overdue bool
	// ReservedMicros es lo apartado en el bolsillo para la tarjeta: ya está
	// reservado para pagarla, y se descuenta de lo que sale de la cuenta.
	ReservedMicros int64
}

// fromAccount es lo que el pago saca de la cuenta, descontado lo apartado.
func (c *CardDue) fromAccount(minimum bool) int64 {
	due := c.TotalMicros
	if minimum {
		due = c.MinimumMicros
	}
	if due -= c.ReservedMicros; due < 0 {
		return 0
	}
	return due
}

// Input es lo que se sabe hoy.
type Input struct {
	// Today es la fecha de hoy en la zona del titular, a medianoche.
	Today         time.Time
	BalanceMicros int64
	Currency      string
	// Recent son los movimientos reales de los últimos SpendWindow días.
	Recent []Movement
	// HistoryDays: cuántos de esos días tiene la cuenta.
	HistoryDays int
	Income      *Income
	Card        *CardDue
}

// Point es un saldo en una fecha.
type Point struct {
	On           time.Time
	AmountMicros int64
}

// Option es una recomendación. No se ejecuta: la persona decide.
type Option struct {
	// Kind: "pay_minimum" (pagar el mínimo y el resto al cobrar), "spend_cap"
	// (gastar hasta tanto por día) o "add_funds" (cuánto falta).
	Kind         string
	AmountMicros int64
	// RestMicros, en "pay_minimum": lo que queda por pagar después.
	RestMicros int64
}

// Forecast es la previsión.
type Forecast struct {
	Today time.Time
	// Until es hasta cuándo se proyecta: el día del próximo ingreso, o
	// DefaultHorizon días si no hay uno que se pueda fechar.
	Until            time.Time
	BalanceMicros    int64
	Currency         string
	DailySpendMicros int64
	// LowHistory: el gasto diario sale de pocos días de historia.
	LowHistory bool
	Income     *Income
	Card       *CardDue
	// Lowest es el punto más bajo antes de Until, pagando la tarjeta entera.
	Lowest Point
	// AfterCard es el saldo el día que vence la tarjeta, recién pagada entera: el
	// «te dejaría con −40». Nil si la tarjeta no vence antes de Until.
	AfterCard *Point
	// Status: "ok", "tight" (se llega, pero justo) o "deficit".
	Status string
	// Cause, si no es "ok": "card" si lo que lo hunde es el pago de la tarjeta,
	// "spending" si es el gasto.
	Cause   string
	Options []Option
}

// refunds son entradas que devuelven un gasto: cuentan contra él, no como ingreso.
var refunds = map[string]bool{
	"authorization_release": true, "authorization_refund": true, "rail_outbound_reverse": true,
}

// excluded no son gasto del día a día: el pago de la tarjeta se proyecta aparte,
// usar la tarjeta no mueve el saldo de la cuenta, y apartar en un bolsillo no es
// gastar (los movimientos de bolsillo ya no llegan aquí; se excluyen igual).
var excluded = map[string]bool{"credit_payment": true, "credit_drawdown": true, "pocket_transfer": true}

// Project calcula la previsión. Es una función pura: los mismos datos dan la
// misma previsión.
func Project(in Input) Forecast {
	f := Forecast{
		Today: in.Today, BalanceMicros: in.BalanceMicros, Currency: in.Currency,
		Income: in.Income, Card: in.Card,
		DailySpendMicros: dailySpend(in.Recent, in.HistoryDays),
		LowHistory:       in.HistoryDays < MinHistoryDays,
	}
	f.Until = in.Today.AddDate(0, 0, DefaultHorizon)
	if in.Income != nil && in.Income.ExpectedOn.After(in.Today) {
		f.Until = in.Income.ExpectedOn
	}
	// La tarjeta solo cuenta si vence antes de que llegue el ingreso: si vence
	// después, se paga con él.
	card := in.Card
	if card != nil && !card.DueOn.Before(f.Until) {
		card = nil
	}

	total, afterCard := walk(in.BalanceMicros, f.DailySpendMicros, in.Today, f.Until, card, false)
	f.Lowest, f.AfterCard = total, afterCard
	spendOnly, _ := walk(in.BalanceMicros, f.DailySpendMicros, in.Today, f.Until, nil, false)

	switch {
	case total.AmountMicros < 0:
		f.Status = "deficit"
	case total.AmountMicros < TightDays*f.DailySpendMicros:
		f.Status = "tight"
	default:
		f.Status = "ok"
		return f
	}
	f.Cause = "spending"
	if card != nil && spendOnly.AmountMicros >= 0 && total.AmountMicros < spendOnly.AmountMicros {
		f.Cause = "card"
	}
	if f.Status == "deficit" {
		f.Options = options(in, f, card)
	}
	return f
}

// walk recorre los días hasta `until` y devuelve el punto más bajo, y el saldo
// el día que se paga la tarjeta. El día del ingreso no se cuenta: el ingreso
// llega ese día.
func walk(balance, daily int64, today, until time.Time, card *CardDue, minimum bool) (Point, *Point) {
	low := Point{On: today, AmountMicros: balance}
	var paid *Point
	pay := func(day time.Time) {
		if card != nil && sameDay(day, maxDay(card.DueOn, today)) {
			balance -= card.fromAccount(minimum)
			paid = &Point{On: day, AmountMicros: balance}
		}
	}
	pay(today)
	if balance < low.AmountMicros {
		low = Point{On: today, AmountMicros: balance}
	}
	for day := today.AddDate(0, 0, 1); day.Before(until); day = day.AddDate(0, 0, 1) {
		balance -= daily
		pay(day)
		if balance < low.AmountMicros {
			low = Point{On: day, AmountMicros: balance}
		}
	}
	return low, paid
}

func options(in Input, f Forecast, card *CardDue) []Option {
	var out []Option
	if card != nil {
		minimum, _ := walk(in.BalanceMicros, f.DailySpendMicros, in.Today, f.Until, card, true)
		if minimum.AmountMicros >= 0 {
			// Pagando el mínimo se llega: el resto se paga al cobrar, y genera
			// intereses mientras tanto. La persona decide si le conviene.
			out = append(out, Option{Kind: "pay_minimum", AmountMicros: card.MinimumMicros,
				RestMicros: card.TotalMicros - card.MinimumMicros})
		} else {
			out = append(out, Option{Kind: "add_funds", AmountMicros: -minimum.AmountMicros})
		}
	}
	// Cuánto se puede gastar por día para no quedar en negativo, pagando la
	// tarjeta entera. Si ni sin gastar alcanza, no hay tope que proponer.
	days := spendDays(in.Today, f.Until)
	committed := int64(0)
	if card != nil {
		committed = card.fromAccount(false)
	}
	if days > 0 && in.BalanceMicros-committed >= 0 {
		limit := (in.BalanceMicros - committed) / int64(days)
		if limit < f.DailySpendMicros {
			out = append(out, Option{Kind: "spend_cap", AmountMicros: limit})
		}
	}
	if card == nil && len(out) == 0 {
		out = append(out, Option{Kind: "add_funds", AmountMicros: -f.Lowest.AmountMicros})
	}
	return out
}

// dailySpend es lo que sale por día, en promedio, descontadas las devoluciones.
func dailySpend(recent []Movement, historyDays int) int64 {
	var spent int64
	for _, m := range recent {
		switch {
		case excluded[m.Kind]:
		case m.Out:
			spent += m.AmountMicros
		case refunds[m.Kind]:
			spent -= m.AmountMicros
		}
	}
	if spent <= 0 {
		return 0
	}
	days := historyDays
	if days > SpendWindow {
		days = SpendWindow
	}
	if days < 1 {
		days = 1
	}
	return spent / int64(days)
}

// spendDays: los días con gasto entre mañana y el día antes de `until`.
func spendDays(today, until time.Time) int {
	n := 0
	for day := today.AddDate(0, 0, 1); day.Before(until); day = day.AddDate(0, 0, 1) {
		n++
	}
	return n
}

func sameDay(a, b time.Time) bool {
	return a.Year() == b.Year() && a.YearDay() == b.YearDay()
}

func maxDay(a, b time.Time) time.Time {
	if a.Before(b) {
		return b
	}
	return a
}
