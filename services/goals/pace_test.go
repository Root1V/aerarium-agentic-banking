package goals

import (
	"testing"
	"time"
)

func day(s string) time.Time {
	d, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return d
}

func TestElAporteMensualCaeElMismoDiaOElUltimoDelMes(t *testing.T) {
	cases := []struct {
		anchor   int
		from, to string
	}{
		{15, "2026-11-15", "2026-12-15"},
		{31, "2026-01-31", "2026-02-28"},
		{31, "2026-02-28", "2026-03-31"},
		{30, "2027-12-30", "2028-01-30"},
	}
	for _, c := range cases {
		if got := nextRun(Monthly, c.anchor, day(c.from)).Format(time.DateOnly); got != c.to {
			t.Errorf("día %d después de %s: %s, se esperaba %s", c.anchor, c.from, got, c.to)
		}
	}
	if got := nextRun(Weekly, 0, day("2026-12-28")).Format(time.DateOnly); got != "2027-01-04" {
		t.Errorf("semanal: %s", got)
	}
}

func TestUnAportePorPeriodo(t *testing.T) {
	if p := periodOf(Weekly, day("2027-01-01")); p != "2026-W53" {
		t.Errorf("la semana ISO del 1 de enero de 2027: %s", p)
	}
	if p := periodOf(Monthly, day("2026-11-07")); p != "2026-11" {
		t.Errorf("mes: %s", p)
	}
}

func TestElRitmoDiceCuandoSeLlegaYCuantoHaceFalta(t *testing.T) {
	today := day("2026-11-07")
	date := day("2026-12-31")
	g := Goal{TargetMicros: 100_000_000, SavedMicros: 10_000_000, TargetDate: &date,
		Rule: &Rule{AmountMicros: 20_000_000, Cadence: Weekly, NextRunOn: day("2026-11-14")}}

	// Faltan 90: cinco aportes de 20, el 14, 21, 28 de noviembre, 5 y 12 de diciembre.
	p := PaceOf(g, today)
	if p.RemainingMicros != 90_000_000 || p.ReachOn == nil || p.ReachOn.Format(time.DateOnly) != "2026-12-12" {
		t.Fatalf("ritmo: %+v", p)
	}
	if p.OnTrack == nil || !*p.OnTrack {
		t.Errorf("llega antes del 31: %+v", p)
	}
	// Hay siete aportes hasta el 31 (del 14 de noviembre al 26 de diciembre):
	// 90 / 7 = 12,86, que se redondea a 13.
	if p.SuggestedMicros != 13_000_000 || p.SuggestedCadence != Weekly {
		t.Errorf("sugerido: %d %s", p.SuggestedMicros, p.SuggestedCadence)
	}

	tight := day("2026-11-30")
	g.TargetDate = &tight
	if p := PaceOf(g, today); p.OnTrack == nil || *p.OnTrack {
		t.Errorf("no llega al 30 de noviembre: %+v", p)
	}

	// Sin regla, sugiere un aporte mensual desde hoy.
	g.Rule, g.TargetDate = nil, &date
	if p := PaceOf(g, today); p.ReachOn != nil || p.OnTrack != nil || p.SuggestedMicros != 45_000_000 ||
		p.SuggestedCadence != Monthly {
		t.Errorf("sin regla: %+v", p)
	}

	g.SavedMicros = 100_000_000
	if p := PaceOf(g, today); p.RemainingMicros != 0 || p.SuggestedMicros != 0 {
		t.Errorf("cumplida: %+v", p)
	}
}
