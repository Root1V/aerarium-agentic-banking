"""Política de crédito: del puntaje a un límite en dinero.

Está separada del modelo a propósito. El puntaje mide riesgo; el límite es una
decisión de negocio que cambia con el apetito de riesgo, el costo del fondeo o una
instrucción del regulador — y debe poder cambiarse sin tocar el modelo.

El principio que la gobierna es el que la evidencia de la región respalda:
**empezar bajo y crecer con el comportamiento**. Un límite inicial generoso a
quien no tiene historial es la forma más rápida de construir una cartera mala.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import Enum

from .money import to_units_floor
from .scoring import Score


class Outcome(str, Enum):
    APPROVED = "approved"
    #: Ni aprobar ni rechazar: falta información para decidir.
    NEEDS_MORE_HISTORY = "needs_more_history"
    DECLINED = "declined"


#: Puntaje mínimo para otorgar crédito.
APPROVAL_THRESHOLD = 550
#: Por debajo de esto se rechaza, pero SOLO si hubo suficiente observación.
DECLINE_THRESHOLD = 350

#: Meses mínimos de relación para poder emitir un rechazo.
#:
#: Rechazar por AUSENCIA de evidencia no es lo mismo que rechazar por evidencia
#: mala, y confundirlas hace daño real: un rechazo queda en el historial de la
#: persona y le dice que no es sujeta de crédito, cuando lo único cierto es que
#: todavía no hay nada que evaluar. Sin observación suficiente la respuesta es
#: pedir más historial, por bajo que salga el puntaje.
MIN_MONTHS_FOR_DECLINE = 3

#: Límite mínimo con el que tiene sentido abrir una línea: 200 unidades.
MIN_LIMIT_MICROS = 200_000_000
#: Techo del primer límite (5.000 unidades), por alto que sea el puntaje. Nadie
#: estrena una línea grande: primero hay que ver cómo paga.
MAX_INITIAL_LIMIT_MICROS = 5_000_000_000

#: Proporción máxima del ingreso mensual observado que puede comprometerse.
#: Es la restricción que impide prestar contra un ingreso que no existe.
MAX_INCOME_SHARE = 0.30


@dataclass(frozen=True, slots=True)
class CreditDecision:
    outcome: Outcome
    limit_micros: int
    currency: str
    score: int
    model_version: str
    reasons: tuple[str, ...]

    @property
    def approved(self) -> bool:
        return self.outcome is Outcome.APPROVED


def decide(
    score_result: Score,
    *,
    average_monthly_inflow_micros: int,
    months_of_history: int,
    currency: str,
) -> CreditDecision:
    """Traduce un puntaje en una decisión con su límite."""
    observed_enough = (
        months_of_history >= MIN_MONTHS_FOR_DECLINE and average_monthly_inflow_micros > 0
    )

    if score_result.value < DECLINE_THRESHOLD and observed_enough:
        return CreditDecision(
            outcome=Outcome.DECLINED,
            limit_micros=0,
            currency=currency,
            score=score_result.value,
            model_version=score_result.model_version,
            reasons=score_result.reason_codes(),
        )

    if score_result.value < APPROVAL_THRESHOLD or not observed_enough:
        # Zona intermedia, o sin observación suficiente para decidir. En ambos
        # casos se invita a seguir usando la cuenta, que es lo que genera la
        # evidencia que hoy falta.
        return CreditDecision(
            outcome=Outcome.NEEDS_MORE_HISTORY,
            limit_micros=0,
            currency=currency,
            score=score_result.value,
            model_version=score_result.model_version,
            reasons=score_result.reason_codes(),
        )

    limit = _limit_for(score_result.value, average_monthly_inflow_micros, months_of_history)

    if limit < MIN_LIMIT_MICROS:
        # Un límite irrisorio no sirve a nadie y sí genera costo de gestión.
        return CreditDecision(
            outcome=Outcome.NEEDS_MORE_HISTORY,
            limit_micros=0,
            currency=currency,
            score=score_result.value,
            model_version=score_result.model_version,
            reasons=score_result.reason_codes(),
        )

    return CreditDecision(
        outcome=Outcome.APPROVED,
        limit_micros=limit,
        currency=currency,
        score=score_result.value,
        model_version=score_result.model_version,
        reasons=(),
    )


def _limit_for(score: int, average_monthly_inflow_micros: int, months_of_history: int) -> int:
    """Calcula el límite inicial.

    Manda siempre la restricción más dura de las tres: el ingreso observado, el
    techo del primer límite y una rampa por antigüedad. El ingreso es la que
    importa — el límite se otorga contra dinero que el banco vio entrar, no contra
    un puntaje alto.
    """
    by_income = int(average_monthly_inflow_micros * MAX_INCOME_SHARE)

    # El puntaje modula dentro de lo que el ingreso permite.
    score_factor = min(1.0, (score - APPROVAL_THRESHOLD) / 300 + 0.4)
    by_score = int(MAX_INITIAL_LIMIT_MICROS * score_factor)

    # Rampa por antigüedad: seis meses de relación para acceder al máximo.
    tenure_factor = min(1.0, months_of_history / 6)
    by_tenure = int(MAX_INITIAL_LIMIT_MICROS * tenure_factor)

    limit = min(by_income, by_score, by_tenure, MAX_INITIAL_LIMIT_MICROS)

    # Se redondea hacia abajo a la unidad mayor para no ofrecer límites con
    # decimales. Hacia abajo, no al más cercano: redondear hacia arriba prestaría
    # dinero que ninguna de las tres restricciones autorizó.
    return to_units_floor(limit)
