"""El próximo ingreso: cuánto suele ser y cuándo se espera.

Lo usa la previsión de saldo. El criterio de qué es un ingreso recurrente es el de
la evaluación de crédito; aquí se prueba el ritmo que se le añade.
"""

from __future__ import annotations

from datetime import date, datetime

from aibank_risk.features import Direction, Movement, extract
from aibank_risk.income import next_income


def paid(day: date, amount: int = 1_500_000_000, who: str = "empleador", kind: str = "rail_inbound_credit") -> Movement:
    return Movement(occurred_at=datetime.combine(day, datetime.min.time()).replace(hour=9),
                    direction=Direction.IN, amount_micros=amount, counterparty=who, kind=kind)


def monthly(days: list[date], **kw) -> list[Movement]:
    return [paid(d, **kw) for d in days]


def test_un_sueldo_mensual_se_espera_el_mismo_dia_del_mes_siguiente() -> None:
    movements = monthly([date(2026, 8, 30), date(2026, 9, 30), date(2026, 10, 30)])
    income = next_income(movements, as_of=date(2026, 11, 2))
    assert income is not None
    assert income.cadence == "monthly"
    assert income.next_expected_on == date(2026, 11, 30)
    assert income.typical_amount_micros == 1_500_000_000
    assert income.occurrences == 3


def test_el_31_cae_en_el_ultimo_dia_de_un_mes_mas_corto() -> None:
    movements = monthly([date(2026, 8, 31), date(2026, 9, 30), date(2026, 10, 31)])
    income = next_income(movements, as_of=date(2026, 11, 1))
    assert income is not None and income.next_expected_on == date(2026, 11, 30)


def test_un_mes_con_extra_no_mueve_lo_que_se_espera() -> None:
    movements = [paid(date(2026, 8, 30), 1_500_000_000), paid(date(2026, 9, 30), 1_700_000_000),
                 paid(date(2026, 10, 30), 1_500_000_000)]
    income = next_income(movements, as_of=date(2026, 11, 2))
    assert income is not None and income.typical_amount_micros == 1_500_000_000


def test_un_pago_quincenal_se_espera_a_los_catorce_dias() -> None:
    movements = monthly([date(2026, 10, 1), date(2026, 10, 15), date(2026, 10, 29)], amount=700_000_000)
    income = next_income(movements, as_of=date(2026, 10, 31))
    assert income is not None
    assert income.cadence == "biweekly"
    assert income.next_expected_on == date(2026, 11, 12)


def test_un_sueldo_que_se_atrasa_unos_dias_se_sigue_esperando_hoy() -> None:
    movements = monthly([date(2026, 8, 25), date(2026, 9, 25), date(2026, 10, 25)])
    income = next_income(movements, as_of=date(2026, 11, 28))
    assert income is not None and income.next_expected_on == date(2026, 11, 28)


def test_un_sueldo_que_dejo_de_llegar_no_se_promete() -> None:
    movements = monthly([date(2026, 6, 25), date(2026, 7, 25), date(2026, 8, 25)])
    assert next_income(movements, as_of=date(2026, 10, 15)) is None


def test_sin_ritmo_reconocible_no_se_fecha() -> None:
    # Tres cobros del mismo origen e importe, pero a los 5 y a los 50 días.
    movements = monthly([date(2026, 8, 1), date(2026, 8, 6), date(2026, 9, 25)])
    assert next_income(movements, as_of=date(2026, 10, 1)) is None


def test_el_criterio_de_ingreso_recurrente_es_el_de_la_evaluacion() -> None:
    # Transferencias entre personas no cuentan como sueldo, ni para el crédito ni
    # para la previsión.
    movements = monthly([date(2026, 8, 30), date(2026, 9, 30), date(2026, 10, 30)], kind="p2p_transfer")
    assert next_income(movements, as_of=date(2026, 11, 2)) is None
    assert not extract(movements, as_of=date(2026, 11, 2)).has_recurring_income

    payroll = monthly([date(2026, 8, 30), date(2026, 9, 30), date(2026, 10, 30)])
    assert next_income(payroll, as_of=date(2026, 11, 2)) is not None
    assert extract(payroll, as_of=date(2026, 11, 2)).has_recurring_income


def test_entre_dos_ingresos_se_toma_el_que_sigue_vivo() -> None:
    old = monthly([date(2026, 3, 5), date(2026, 4, 5), date(2026, 5, 5)], who="empleo-anterior")
    new = monthly([date(2026, 8, 28), date(2026, 9, 28), date(2026, 10, 28)], who="empleo-actual")
    income = next_income(old + new, as_of=date(2026, 11, 1))
    assert income is not None and income.counterparty == "empleo-actual"
