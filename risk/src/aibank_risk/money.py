"""La unidad monetaria del sistema.

Todo importe de AIBank es un entero en **micras**: millonésimas (10⁻⁶) de la
unidad mayor. Un dólar son ``1_000_000``; un centavo, ``10_000``.

El motor de crédito hereda la escala del ledger a propósito. Podría trabajar en
otra y convertir en la frontera, y sería un error: una decisión de crédito se
audita contra los asientos que la justificaron, y dos escalas distintas hacen que
esa comparación necesite una conversión — que es exactamente donde se cuela la
diferencia de un centavo que después nadie sabe explicar.

Ver ``core/src/money.rs`` para el razonamiento completo de por qué la escala es 6.
"""

from __future__ import annotations

#: Micras por unidad mayor (1 USD = 1_000_000 micras).
MICROS_PER_UNIT = 1_000_000

#: Micras por centavo.
MICROS_PER_CENT = 10_000


def from_units(units: int) -> int:
    """Convierte unidades mayores enteras a micras."""
    return units * MICROS_PER_UNIT


def to_units_floor(micros: int) -> int:
    """Trunca un importe a la unidad mayor inferior.

    Se usa para no ofrecer un límite de crédito con decimales: "S/ 1.847,3629 de
    línea aprobada" no le dice nada a nadie y da la impresión de un cálculo mal
    hecho, aunque sea exacto.
    """
    return (micros // MICROS_PER_UNIT) * MICROS_PER_UNIT
