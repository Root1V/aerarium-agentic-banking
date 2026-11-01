package forecast_test

import (
	"testing"
	"time"

	"github.com/aibank/aibank/services/forecast"
)

// La previsión con los números del caso 1 del documento: «tu tarjeta vence el
// 16 y te dejaría con −40».

func day(d int) time.Time { return time.Date(2026, 11, d, 0, 0, 0, 0, time.UTC) }

const usd = 1_000_000

// gasto de 10 USD por día durante 30 días.
func spending(perDay int64) []forecast.Movement {
	var out []forecast.Movement
	for i := 0; i < 30; i++ {
		out = append(out, forecast.Movement{At: day(1).AddDate(0, 0, -i), Out: true, AmountMicros: perDay})
	}
	return out
}

func base() forecast.Input {
	return forecast.Input{
		Today: day(1), BalanceMicros: 300 * usd, Currency: "USD",
		Recent: spending(10 * usd), HistoryDays: 30,
		Income: &forecast.Income{AmountMicros: 1500 * usd, ExpectedOn: day(30), Cadence: "monthly"},
	}
}

func TestLaTarjetaQueVenceAntesDelSueldoDejaEnNegativo(t *testing.T) {
	in := base()
	// 300 de saldo, 10 por día hasta el 16 (15 días) = 150; la tarjeta, 190.
	in.Card = &forecast.CardDue{DueOn: day(16), MinimumMicros: 20 * usd, TotalMicros: 190 * usd}
	f := forecast.Project(in)

	if f.Status != "deficit" || f.Cause != "card" {
		t.Fatalf("estado: %s por %s", f.Status, f.Cause)
	}
	// El 16, al pagar: 300 − 150 − 190 = −40. Es el «te dejaría con −40».
	if f.AfterCard == nil || f.AfterCard.AmountMicros != -40*usd || !f.AfterCard.On.Equal(day(16)) {
		t.Errorf("al pagar la tarjeta: %+v", f.AfterCard)
	}
	// Y a ese ritmo sigue bajando hasta el día antes del sueldo.
	if f.Lowest.AmountMicros != -170*usd || !f.Lowest.On.Equal(day(29)) {
		t.Errorf("punto más bajo: %+v", f.Lowest)
	}
	if f.Until != day(30) || f.DailySpendMicros != 10*usd {
		t.Errorf("horizonte y gasto: %v %d", f.Until, f.DailySpendMicros)
	}
	// Con el mínimo se llega justo a 0: es lo primero que se propone. Y un tope
	// de gasto con el que alcanzaría pagando entera: (300 − 190) / 28 días.
	if len(f.Options) != 2 || f.Options[0].Kind != "pay_minimum" ||
		f.Options[1].Kind != "spend_cap" || f.Options[1].AmountMicros != 110*usd/28 {
		t.Errorf("opciones: %+v", f.Options)
	}
}

// Si pagar el mínimo alcanza, eso es lo primero que se propone, con el resto.
func TestSiElMinimoAlcanzaSeProponePagarloYElRestoAlCobrar(t *testing.T) {
	in := base()
	in.BalanceMicros = 400 * usd
	in.Card = &forecast.CardDue{DueOn: day(16), MinimumMicros: 20 * usd, TotalMicros: 190 * usd}
	f := forecast.Project(in)

	// 400 − 280 de gasto hasta el 29 − 190 = −70. Con el mínimo: 400 − 280 − 20 = 100.
	if f.Status != "deficit" || f.Lowest.AmountMicros != -70*usd {
		t.Fatalf("previsión: %s %d", f.Status, f.Lowest.AmountMicros)
	}
	first := f.Options[0]
	if first.Kind != "pay_minimum" || first.AmountMicros != 20*usd || first.RestMicros != 170*usd {
		t.Errorf("primera opción: %+v", first)
	}
}

func TestSinTarjetaElGastoSoloTambienAvisa(t *testing.T) {
	in := base()
	in.BalanceMicros = 200 * usd // 10 por día durante 28 días = 280
	f := forecast.Project(in)
	if f.Status != "deficit" || f.Cause != "spending" {
		t.Fatalf("estado: %s por %s", f.Status, f.Cause)
	}
	cap := f.Options[0]
	if cap.Kind != "spend_cap" || cap.AmountMicros != 200*usd/28 {
		t.Errorf("tope diario: %+v", cap)
	}
}

func TestLlegarConPocoMargenEsJusto(t *testing.T) {
	in := base()
	in.BalanceMicros = 310 * usd // quedan 30, menos de cinco días de gasto
	f := forecast.Project(in)
	if f.Status != "tight" || f.Lowest.AmountMicros != 30*usd || len(f.Options) != 0 {
		t.Errorf("previsión: %+v", f)
	}
}

func TestConHolguraNoHayNadaQueAvisar(t *testing.T) {
	in := base()
	in.BalanceMicros = 500 * usd
	f := forecast.Project(in)
	// 500 − 280 = 220 el día antes del sueldo.
	if f.Status != "ok" || f.Lowest.AmountMicros != 220*usd || len(f.Options) != 0 {
		t.Errorf("previsión: %+v", f)
	}
}

// La tarjeta que vence después del sueldo se paga con el sueldo: no cuenta.
func TestLaTarjetaQueVenceDespuesDelSueldoNoCuenta(t *testing.T) {
	in := base()
	in.BalanceMicros = 1000 * usd
	in.Card = &forecast.CardDue{DueOn: day(30), MinimumMicros: 20 * usd, TotalMicros: 900 * usd}
	if f := forecast.Project(in); f.Status != "ok" || f.Card == nil {
		t.Errorf("previsión: %s, tarjeta %v", f.Status, f.Card)
	}
}

func TestSinIngresoSeProyectanTreintaDias(t *testing.T) {
	in := base()
	in.Income = nil
	f := forecast.Project(in)
	if !f.Until.Equal(day(1).AddDate(0, 0, forecast.DefaultHorizon)) {
		t.Errorf("horizonte: %v", f.Until)
	}
}

// Lo que entra devuelto no es ingreso: descuenta el gasto. Y pagar la tarjeta no
// es gasto del día a día: se proyecta aparte.
func TestElGastoDiarioDescuentaDevolucionesYNoCuentaLaTarjeta(t *testing.T) {
	in := base()
	in.Recent = append(spending(10*usd),
		forecast.Movement{Out: false, AmountMicros: 30 * usd, Kind: "authorization_refund"},
		forecast.Movement{Out: true, AmountMicros: 500 * usd, Kind: "credit_payment"},
	)
	if f := forecast.Project(in); f.DailySpendMicros != 9*usd {
		t.Errorf("gasto diario: %d", f.DailySpendMicros)
	}
}

func TestConPocaHistoriaLaPrevisionLoDice(t *testing.T) {
	in := base()
	in.HistoryDays = 5
	if f := forecast.Project(in); !f.LowHistory {
		t.Error("cinco días de historia y no lo dice")
	}
}

// Si ni con el mínimo alcanza, se dice cuánto falta para pagarlo.
func TestSiNiElMinimoAlcanzaSeDiceCuantoFalta(t *testing.T) {
	in := base()
	in.BalanceMicros = 100 * usd
	in.Card = &forecast.CardDue{DueOn: day(16), MinimumMicros: 20 * usd, TotalMicros: 190 * usd}
	f := forecast.Project(in)
	// 100 − 280 − 20 = −200.
	if len(f.Options) == 0 || f.Options[0].Kind != "add_funds" || f.Options[0].AmountMicros != 200*usd {
		t.Errorf("opciones: %+v", f.Options)
	}
}

// Una tarjeta vencida sin pagar se cuenta hoy.
func TestUnaTarjetaVencidaSeCuentaHoy(t *testing.T) {
	in := base()
	in.BalanceMicros = 1000 * usd
	in.Card = &forecast.CardDue{DueOn: day(1).AddDate(0, 0, -3), MinimumMicros: 20 * usd, TotalMicros: 100 * usd, Overdue: true}
	f := forecast.Project(in)
	if f.AfterCard == nil || !f.AfterCard.On.Equal(day(1)) || f.AfterCard.AmountMicros != 900*usd {
		t.Errorf("tarjeta vencida: %+v", f.AfterCard)
	}
}
