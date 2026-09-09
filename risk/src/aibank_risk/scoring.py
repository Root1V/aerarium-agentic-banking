"""Modelo de scoring crediticio.

**Por qué una tarjeta de puntuación y no un modelo de aprendizaje automático.**

No hay con qué entrenar: sin cartera colocada no existen incumplimientos
observados, y un modelo ajustado sobre datos que no se tienen es teatro. Los
emisores reales empiezan con una tarjeta de puntuación experta y migran a modelos
estadísticos cuando la cartera ya produjo comportamiento que aprender.

Además hay dos exigencias que un modelo opaco no cumple hoy:

- **Explicabilidad.** A quien se le niega un crédito hay que poder decirle por qué,
  y a un supervisor hay que poder mostrarle la regla. "El modelo dijo que no" no
  es una respuesta admisible.
- **Reproducibilidad.** Las mismas entradas y la misma versión tienen que dar
  exactamente el mismo resultado, años después, para poder auditar una decisión.

La estructura está pensada para que sustituir esta tarjeta por un modelo entrenado
sea cambiar `score()` conservando el contrato: puntaje, contribuciones y motivos.
"""

from __future__ import annotations

from dataclasses import dataclass

from .features import Features

#: Versión del modelo. Se registra en cada decisión: sin ella no se puede
#: reconstruir con qué reglas se resolvió un caso del pasado.
MODEL_VERSION = "scorecard-v1"

MIN_SCORE = 0
MAX_SCORE = 1000


@dataclass(frozen=True, slots=True)
class Contribution:
    """Cuánto aportó una característica al puntaje."""

    feature: str
    points: int
    #: Máximo alcanzable, para saber cuánto se dejó de sumar.
    max_points: int
    explanation: str

    @property
    def shortfall(self) -> int:
        return self.max_points - self.points


@dataclass(frozen=True, slots=True)
class Score:
    value: int
    model_version: str
    contributions: tuple[Contribution, ...]

    def reason_codes(self, limit: int = 3) -> tuple[str, ...]:
        """Motivos principales por los que el puntaje no fue mayor.

        Se ordenan por cuánto puntaje se dejó de sumar, que es exactamente lo que
        hay que comunicar: qué habría que mejorar para calificar.
        """
        hurting = [c for c in self.contributions if c.shortfall > 0]
        hurting.sort(key=lambda c: c.shortfall, reverse=True)
        return tuple(c.explanation for c in hurting[:limit])


def _band(value: float, bands: list[tuple[float, int]]) -> int:
    """Devuelve los puntos del primer umbral que el valor alcanza.

    Las bandas van de mejor a peor y el último tramo actúa de piso.
    """
    for threshold, points in bands:
        if value >= threshold:
            return points
    return bands[-1][1]


def score(features: Features) -> Score:
    """Puntúa una cuenta a partir de lo observado en ella.

    Los pesos reflejan lo que la evidencia de la industria señala como predictivo:
    el ingreso recurrente y su regularidad pesan más que el monto, porque la
    capacidad de pago sostenida importa más que un mes bueno.
    """
    contributions: list[Contribution] = []

    # Antigüedad: sin historia no hay nada que observar.
    history_points = _band(
        features.months_of_history,
        [(12, 150), (6, 110), (3, 60), (1, 25), (0, 0)],
    )
    contributions.append(
        Contribution(
            "months_of_history",
            history_points,
            150,
            "Tu cuenta es demasiado nueva para evaluar tu comportamiento",
        )
    )

    # Ingreso recurrente: el indicador con mayor valor predictivo.
    recurring_points = 200 if features.has_recurring_income else 0
    contributions.append(
        Contribution(
            "has_recurring_income",
            recurring_points,
            200,
            "No detectamos un ingreso que se repita mes a mes en tu cuenta",
        )
    )

    # Regularidad: cobrar todos los meses vale más que cobrar mucho una vez.
    regularity_points = _band(
        features.inflow_regularity,
        [(0.75, 200), (0.5, 150), (0.3, 90), (0.1, 40), (0.0, 0)],
    )
    contributions.append(
        Contribution(
            "inflow_regularity",
            regularity_points,
            200,
            "Tus ingresos varían mucho de un mes a otro",
        )
    )

    # Monto del ingreso: importa, pero menos que su constancia.
    inflow_points = _band(
        features.average_monthly_inflow_micros,
        [(3_000_000_000, 180), (1_500_000_000, 140), (800_000_000, 100), (300_000_000, 55), (0, 0)],
    )
    contributions.append(
        Contribution(
            "average_monthly_inflow_micros",
            inflow_points,
            180,
            "El ingreso mensual que observamos en tu cuenta es bajo",
        )
    )

    # Cuánto se gasta de lo que entra: gastar todo no deja margen para pagar.
    ratio = features.outflow_to_inflow_ratio
    if ratio == 0.0 and features.average_monthly_inflow_micros == 0:
        margin_points = 0
    elif ratio <= 0.7:
        margin_points = 120
    elif ratio <= 0.85:
        margin_points = 85
    elif ratio <= 0.95:
        margin_points = 45
    else:
        margin_points = 0
    contributions.append(
        Contribution(
            "outflow_to_inflow_ratio",
            margin_points,
            120,
            "Gastas casi todo lo que ingresas, sin margen para una cuota",
        )
    )

    # Saldo en cero: señal directa de tensión de liquidez.
    zero_points = _band(
        1.0 - features.zero_balance_ratio,
        [(0.9, 90), (0.7, 65), (0.5, 35), (0.0, 0)],
    )
    contributions.append(
        Contribution(
            "zero_balance_ratio",
            zero_points,
            90,
            "Tu cuenta pasa muchos días sin saldo disponible",
        )
    )

    # Arraigo: una cuenta que se usa con varias contrapartes es la cuenta principal.
    counterparty_points = _band(
        features.distinct_counterparties,
        [(10, 60), (5, 45), (2, 25), (0, 0)],
    )
    contributions.append(
        Contribution(
            "distinct_counterparties",
            counterparty_points,
            60,
            "Usas poco esta cuenta para tu día a día",
        )
    )

    # Rechazos por falta de fondos: penalización, no puntaje a ganar.
    penalty = min(features.insufficient_funds_events * 25, 100)
    contributions.append(
        Contribution(
            "insufficient_funds_events",
            -penalty,
            0,
            "Registramos intentos de pago rechazados por falta de fondos",
        )
    )

    total = sum(c.points for c in contributions)
    return Score(
        value=max(MIN_SCORE, min(MAX_SCORE, total)),
        model_version=MODEL_VERSION,
        contributions=tuple(contributions),
    )
