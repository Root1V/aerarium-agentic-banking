"""Evaluación crediticia completa y su expediente.

Una decisión de crédito no es solo un sí o un no: es un acto que hay que poder
reconstruir. Ante un reclamo o una revisión del supervisor debe poderse mostrar
qué se observó, con qué versión del modelo se evaluó y por qué salió lo que salió.

Por eso la unidad de salida no es un número sino un expediente completo.
"""

from __future__ import annotations

import dataclasses
import json
from dataclasses import dataclass
from datetime import date, datetime
from typing import Any

from .fairness import assert_permitted
from .features import Features, Movement, extract
from .policy import CreditDecision, Outcome, decide
from .scoring import Score, score


@dataclass(frozen=True, slots=True)
class Assessment:
    """Expediente de una evaluación crediticia."""

    customer_id: str
    decision: CreditDecision
    features: Features
    #: Contribución de cada característica al puntaje.
    contributions: tuple[tuple[str, int], ...]
    #: Momento al que se refieren los datos, no cuándo se ejecutó el cálculo.
    as_of: date
    evaluated_at: datetime

    def to_record(self) -> dict[str, Any]:
        """Serializa el expediente para archivarlo.

        Contiene identificadores y agregados de comportamiento, nunca datos
        personales: un expediente de crédito se conserva años y se consulta desde
        varios sistemas.
        """
        return {
            "customer_id": self.customer_id,
            "outcome": self.decision.outcome.value,
            "limit_minor": self.decision.limit_minor,
            "currency": self.decision.currency,
            "score": self.decision.score,
            "model_version": self.decision.model_version,
            "reasons": list(self.decision.reasons),
            "features": dataclasses.asdict(self.features),
            "contributions": {name: points for name, points in self.contributions},
            "as_of": self.as_of.isoformat(),
            "evaluated_at": self.evaluated_at.isoformat(),
        }

    def to_json(self) -> str:
        return json.dumps(self.to_record(), ensure_ascii=False, sort_keys=True)


def assess(
    customer_id: str,
    movements: list[Movement],
    *,
    as_of: date,
    evaluated_at: datetime,
    currency: str = "USD",
    observed_days: int = 180,
    zero_balance_days: int = 0,
    insufficient_funds_events: int = 0,
) -> Assessment:
    """Evalúa a un cliente y devuelve el expediente de la decisión.

    ``as_of`` y ``evaluated_at`` son explícitos y no se toman del reloj: una
    decisión de crédito tiene que poder recalcularse años después exactamente como
    se tomó.
    """
    features = extract(
        movements,
        as_of=as_of,
        observed_days=observed_days,
        zero_balance_days=zero_balance_days,
        insufficient_funds_events=insufficient_funds_events,
    )

    # La barrera de no discriminación está en el camino de ejecución, no solo en
    # las pruebas: una característica añadida más adelante no puede colarse.
    assert_permitted([field.name for field in dataclasses.fields(features)])

    score_result: Score = score(features)
    decision: CreditDecision = decide(
        score_result,
        average_monthly_inflow_minor=features.average_monthly_inflow_minor,
        months_of_history=features.months_of_history,
        currency=currency,
    )

    return Assessment(
        customer_id=customer_id,
        decision=decision,
        features=features,
        contributions=tuple((c.feature, c.points) for c in score_result.contributions),
        as_of=as_of,
        evaluated_at=evaluated_at,
    )


__all__ = ["Assessment", "assess", "Outcome"]
