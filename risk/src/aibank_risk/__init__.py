"""Motor de riesgo crediticio de AIBank.

Evalúa capacidad de pago con el comportamiento observado en la propia cuenta, que
es la única evidencia disponible sobre quien no tiene historial en el buró — el
segmento al que este banco quiere prestarle.

Tres propiedades gobiernan el diseño:

- **Explicable**: toda decisión trae los motivos que la sustentan.
- **Reproducible**: mismas entradas y misma versión dan el mismo resultado.
- **No discriminatoria**: los atributos protegidos no pueden intervenir, y la
  barrera está en el camino de ejecución.
"""

from .assessment import Assessment, assess
from .features import Direction, Features, Movement, extract
from .fairness import ProhibitedFeatureError, is_prohibited
from .policy import CreditDecision, Outcome, decide
from .scoring import MODEL_VERSION, Score, score

__all__ = [
    "MODEL_VERSION",
    "Assessment",
    "CreditDecision",
    "Direction",
    "Features",
    "Movement",
    "Outcome",
    "ProhibitedFeatureError",
    "Score",
    "assess",
    "decide",
    "extract",
    "is_prohibited",
    "score",
]
