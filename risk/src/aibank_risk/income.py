"""Cuándo y cuánto se espera el próximo ingreso.

La evaluación de crédito solo necesita saber SI hay un ingreso recurrente. La
previsión de saldo necesita además cuánto suele ser y cuándo llega el próximo:
«tu tarjeta vence el 16 y cobras el 30» es una frase que exige una fecha.

El criterio de qué es un ingreso recurrente no se repite aquí: es
`features.recurring_groups`, el mismo de la evaluación. Este módulo solo le añade
el ritmo.
"""
from __future__ import annotations

import calendar
import statistics
from dataclasses import dataclass
from datetime import date, timedelta

from .features import Direction, Movement, recurring_groups

#: Ritmos que se reconocen, por los días entre un ingreso y el siguiente.
CADENCES = (
    ("monthly", 25, 35),
    ("biweekly", 12, 17),
    ("weekly", 6, 8),
)

#: Días de atraso tras los que el próximo ingreso deja de darse por esperado.
#: Un sueldo que llega dos días tarde sigue siendo un sueldo; uno que no llega
#: en diez puede haber terminado, y prometerlo sería peor que no decir nada.
MAX_LATE_DAYS = 7


@dataclass(frozen=True, slots=True)
class RecurringIncome:
    counterparty: str
    #: La mediana de lo cobrado: un mes con extra no mueve la previsión.
    typical_amount_micros: int
    occurrences: int
    #: `monthly`, `biweekly` o `weekly`.
    cadence: str
    last_received_on: date
    next_expected_on: date

    def to_record(self) -> dict:
        return {
            "counterparty": self.counterparty,
            "typical_amount_micros": self.typical_amount_micros,
            "occurrences": self.occurrences,
            "cadence": self.cadence,
            "last_received_on": self.last_received_on.isoformat(),
            "next_expected_on": self.next_expected_on.isoformat(),
        }


def next_income(movements: list[Movement], *, as_of: date) -> RecurringIncome | None:
    """El próximo ingreso esperado, o None si no hay uno que se pueda fechar.

    Entre varios ingresos recurrentes se toma el que llegó más recientemente: es
    el que sigue vivo. Sin un ritmo reconocible —llega, pero cada tanto— no se
    fecha, porque la previsión quedaría colgada de una fecha inventada.
    """
    inflows = [m for m in movements if m.direction is Direction.IN]
    groups = recurring_groups(inflows)
    if not groups:
        return None
    group = max(groups, key=lambda g: (g[-1].occurred_at, statistics.median(m.amount_micros for m in g)))

    days = [m.occurred_at.date() for m in group]
    gaps = [(b - a).days for a, b in zip(days, days[1:]) if (b - a).days > 0]
    if not gaps:
        return None
    cadence = _cadence(gaps)
    if cadence is None:
        return None

    last = days[-1]
    expected = _after(last, cadence)
    # Si ya pasó la fecha, se espera igual unos días: los sueldos se atrasan.
    # Pasado ese margen, no se promete.
    if (as_of - expected).days > MAX_LATE_DAYS:
        return None
    expected = max(expected, as_of)

    return RecurringIncome(
        counterparty=group[-1].counterparty,
        typical_amount_micros=int(statistics.median(m.amount_micros for m in group)),
        occurrences=len(group),
        cadence=cadence,
        last_received_on=last,
        next_expected_on=expected,
    )


def _cadence(gaps: list[int]) -> str | None:
    """El ritmo de los ingresos, si al menos dos tercios de los espacios entre uno
    y otro caben en él.

    No basta con la mediana: cobros a los 5 y a los 50 días tienen una mediana de
    27 y no son un sueldo mensual.
    """
    for name, low, high in CADENCES:
        fitting = sum(1 for gap in gaps if low <= gap <= high)
        if fitting * 3 >= len(gaps) * 2:
            return name
    return None


def _after(day: date, cadence: str) -> date:
    if cadence == "weekly":
        return day + timedelta(days=7)
    if cadence == "biweekly":
        return day + timedelta(days=14)
    # Mensual: el mismo día del mes siguiente, o el último si no existe (31 → 30).
    year, month = (day.year + 1, 1) if day.month == 12 else (day.year, day.month + 1)
    return date(year, month, min(day.day, calendar.monthrange(year, month)[1]))
