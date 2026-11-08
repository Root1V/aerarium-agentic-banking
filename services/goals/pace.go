package goals

import "time"

// Pace dice si la meta llega a tiempo al ritmo de su regla, y qué haría falta.
type Pace struct {
	RemainingMicros int64
	// ReachOn: el día del aporte con el que se completa, al ritmo de la regla.
	// Nil sin regla, o si ya está cumplida.
	ReachOn *time.Time
	// OnTrack: llega antes de la fecha de la meta. Nil sin fecha o sin regla.
	OnTrack *bool
	// SuggestedMicros: lo que habría que apartar en cada aporte para llegar a la
	// fecha, redondeado hacia arriba a la unidad. 0 sin fecha.
	SuggestedMicros int64
	// SuggestedCadence: la de la regla, o mensual si no tiene.
	SuggestedCadence Cadence
}

// maxRuns acota la cuenta: una regla de 1 para una meta de un millón no se
// recorre aporte por aporte.
const maxRuns = 520

// PaceOf calcula el ritmo de una meta. Es una función pura.
func PaceOf(g Goal, today time.Time) Pace {
	p := Pace{RemainingMicros: max(0, g.TargetMicros-g.SavedMicros), SuggestedCadence: Monthly}
	if p.RemainingMicros == 0 {
		return p
	}
	cadence, anchor, first := Monthly, today.Day(), today
	if g.Rule != nil {
		cadence, anchor = g.Rule.Cadence, g.Rule.AnchorDay
		if g.Rule.NextRunOn.After(today) {
			first = g.Rule.NextRunOn
		}
	}
	p.SuggestedCadence = cadence

	if g.Rule != nil {
		runs := (p.RemainingMicros + g.Rule.AmountMicros - 1) / g.Rule.AmountMicros
		if runs <= maxRuns {
			day := first
			for i := int64(1); i < runs; i++ {
				day = nextRun(cadence, anchor, day)
			}
			p.ReachOn = &day
		}
	}

	if g.TargetDate != nil {
		count := int64(0)
		for day := first; !day.After(*g.TargetDate) && count < maxRuns; day = nextRun(cadence, anchor, day) {
			count++
		}
		count = max(count, 1)
		suggested := (p.RemainingMicros + count - 1) / count
		p.SuggestedMicros = (suggested + unit - 1) / unit * unit
		if g.Rule != nil {
			onTrack := p.ReachOn != nil && !p.ReachOn.After(*g.TargetDate)
			p.OnTrack = &onTrack
		}
	}
	return p
}
