"""Extracción de características desde el historial transaccional.

La tesis del negocio es prestarle a gente sin historial en el buró de crédito.
Lo único que se sabe de esas personas es cómo se comporta su dinero en la cuenta,
así que estas características SON el activo diferencial del banco.

Todas se calculan sobre movimientos que el propio banco observó. Nada viene de un
tercero, y nada depende de datos declarados por la persona —que es lo que se puede
falsear.
"""

from __future__ import annotations

import statistics
from dataclasses import dataclass
from datetime import date, datetime, timedelta
from enum import Enum


class Direction(str, Enum):
    """Dirección desde el punto de vista del cliente."""

    IN = "in"
    OUT = "out"


@dataclass(frozen=True, slots=True)
class Movement:
    """Un movimiento de la cuenta.

    Los importes son enteros en micras, igual que en el ledger: en un motor de
    crédito un redondeo mal hecho se multiplica por toda la cartera.
    """

    occurred_at: datetime
    direction: Direction
    amount_micros: int
    #: Contraparte, para medir con cuánta gente distinta opera.
    counterparty: str = ""
    #: Naturaleza del movimiento tal como la asentó el core.
    kind: str = ""

    def __post_init__(self) -> None:
        if self.amount_micros <= 0:
            raise ValueError("los importes de un movimiento son positivos")


@dataclass(frozen=True, slots=True)
class Features:
    """Características observadas de una cuenta.

    Son deliberadamente pocas y todas interpretables: cada una tiene que poder
    explicarse a la persona a la que se le niega un crédito.
    """

    #: Antigüedad de la relación, en meses completos.
    months_of_history: int
    #: Ingreso mensual promedio observado, en micras.
    average_monthly_inflow_micros: int
    #: Regularidad del ingreso: 1.0 = idéntico todos los meses, 0.0 = errático.
    inflow_regularity: float
    #: Si se detecta un ingreso recurrente compatible con una nómina.
    has_recurring_income: bool
    #: Proporción de lo que sale respecto de lo que entra.
    outflow_to_inflow_ratio: float
    #: Días con saldo en cero sobre el período observado.
    zero_balance_ratio: float
    #: Cuántas contrapartes distintas: mide arraigo de la cuenta.
    distinct_counterparties: int
    #: Intentos rechazados por falta de fondos. Señal temprana de tensión.
    insufficient_funds_events: int


#: Un mes de calendario aproximado. Se usa un valor fijo para que el cálculo sea
#: reproducible: dos corridas sobre los mismos datos deben dar lo mismo.
DAYS_PER_MONTH = 30


def extract(
    movements: list[Movement],
    *,
    as_of: date,
    observed_days: int = 180,
    zero_balance_days: int = 0,
    insufficient_funds_events: int = 0,
) -> Features:
    """Calcula las características de una cuenta.

    ``as_of`` fija el momento de cálculo. Es explícito y no se toma del reloj para
    que una decisión pueda recalcularse tal como se tomó: un motor de crédito que
    no es reproducible no se puede auditar.
    """
    if observed_days <= 0:
        raise ValueError("el período observado debe ser positivo")

    window_start = datetime.combine(as_of, datetime.min.time()) - timedelta(days=observed_days)
    in_window = [m for m in movements if m.occurred_at >= window_start]

    if not in_window:
        return Features(
            months_of_history=0,
            average_monthly_inflow_micros=0,
            inflow_regularity=0.0,
            has_recurring_income=False,
            outflow_to_inflow_ratio=0.0,
            zero_balance_ratio=1.0,
            distinct_counterparties=0,
            insufficient_funds_events=insufficient_funds_events,
        )

    oldest = min(m.occurred_at for m in movements)
    months = max(
        1,
        int((datetime.combine(as_of, datetime.min.time()) - oldest).days / DAYS_PER_MONTH),
    )

    inflows = [m for m in in_window if m.direction is Direction.IN]
    outflows = [m for m in in_window if m.direction is Direction.OUT]

    monthly = _monthly_totals(inflows, as_of=as_of, observed_days=observed_days)
    total_inflow = sum(m.amount_micros for m in inflows)
    total_outflow = sum(m.amount_micros for m in outflows)

    return Features(
        months_of_history=months,
        average_monthly_inflow_micros=int(statistics.fmean(monthly)) if monthly else 0,
        inflow_regularity=_regularity(monthly),
        has_recurring_income=_detect_recurring_income(inflows),
        # Sin ingresos el cociente no significa nada; se reporta 0 en vez de
        # inventar un infinito que después habría que tratar como caso especial.
        outflow_to_inflow_ratio=(total_outflow / total_inflow) if total_inflow else 0.0,
        zero_balance_ratio=min(1.0, zero_balance_days / observed_days),
        distinct_counterparties=len({m.counterparty for m in in_window if m.counterparty}),
        insufficient_funds_events=insufficient_funds_events,
    )


def _monthly_totals(movements: list[Movement], *, as_of: date, observed_days: int) -> list[int]:
    """Suma los importes por mes del período observado, incluidos los meses en cero.

    Incluir los meses vacíos es esencial: quien cobró una vez y desapareció no
    puede parecerse a quien cobra todos los meses.
    """
    months = max(1, observed_days // DAYS_PER_MONTH)
    end = datetime.combine(as_of, datetime.min.time())
    totals = []
    for index in range(months):
        bucket_end = end - timedelta(days=DAYS_PER_MONTH * index)
        bucket_start = bucket_end - timedelta(days=DAYS_PER_MONTH)
        totals.append(
            sum(m.amount_micros for m in movements if bucket_start <= m.occurred_at < bucket_end)
        )
    return totals


def _regularity(monthly_totals: list[int]) -> float:
    """Traduce la dispersión mensual a una escala de 0 a 1.

    Un ingreso estable predice capacidad de pago mucho mejor que un ingreso alto
    pero errático, que es el perfil típico del trabajo informal.
    """
    positive = [t for t in monthly_totals if t > 0]
    if len(positive) < 2:
        return 0.0

    mean = statistics.fmean(positive)
    if mean == 0:
        return 0.0

    # Coeficiente de variación: dispersión relativa al promedio, así el resultado
    # no depende de si la persona gana mucho o poco.
    variation = statistics.pstdev(positive) / mean
    regularity = max(0.0, 1.0 - variation)

    # Penaliza los meses sin ingreso: la regularidad es cobrar TODOS los meses.
    coverage = len(positive) / len(monthly_totals)
    return round(regularity * coverage, 4)


#: Tolerancia para considerar dos importes "el mismo ingreso" mes a mes.
RECURRING_AMOUNT_TOLERANCE = 0.15


def _detect_recurring_income(inflows: list[Movement]) -> bool:
    """Detecta un ingreso recurrente compatible con una nómina.

    Es la característica de mayor valor predictivo según los datos de la industria:
    la penetración de nómina es el mejor indicador de permanencia y capacidad de
    pago de un cliente.
    """
    payroll_like = [m for m in inflows if m.kind in {"payroll", "rail_inbound_credit", "deposit"}]
    if len(payroll_like) < 3:
        return False

    by_counterparty: dict[str, list[Movement]] = {}
    for movement in payroll_like:
        if movement.counterparty:
            by_counterparty.setdefault(movement.counterparty, []).append(movement)

    for movements in by_counterparty.values():
        if len(movements) < 3:
            continue
        amounts = [m.amount_micros for m in movements]
        mean = statistics.fmean(amounts)
        if mean == 0:
            continue
        # Mismo origen, importe parecido y al menos tres veces: se comporta como
        # un sueldo aunque nadie lo haya declarado como tal.
        if statistics.pstdev(amounts) / mean <= RECURRING_AMOUNT_TOLERANCE:
            return True

    return False
