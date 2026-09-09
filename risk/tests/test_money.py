"""La escala monetaria que el motor de crédito comparte con el ledger."""

from aibank_risk.money import MICROS_PER_CENT, MICROS_PER_UNIT, from_units, to_units_floor
from aibank_risk.policy import MAX_INITIAL_LIMIT_MICROS, MIN_LIMIT_MICROS


class TestEscala:
    def test_la_escala_es_la_misma_del_ledger(self):
        assert MICROS_PER_UNIT == 1_000_000
        assert MICROS_PER_CENT == 10_000
        assert from_units(1) == MICROS_PER_UNIT

    def test_los_limites_de_la_politica_son_montos_reconocibles(self):
        # Si alguien reescala mal una constante, el error aparece aquí y no en
        # una línea de crédito de diez mil veces lo previsto.
        assert MIN_LIMIT_MICROS == from_units(200)
        assert MAX_INITIAL_LIMIT_MICROS == from_units(5_000)


class TestRedondeoDelLimite:
    def test_trunca_a_la_unidad_mayor(self):
        assert to_units_floor(1_847_362_900) == 1_847_000_000
        assert to_units_floor(from_units(200)) == from_units(200)

    def test_redondea_hacia_abajo_y_nunca_hacia_arriba(self):
        # Un céntimo por encima de 200 sigue siendo una línea de 200: redondear
        # hacia arriba prestaría dinero que la política no autorizó.
        assert to_units_floor(from_units(200) + 1) == from_units(200)
        assert to_units_floor(from_units(201) - 1) == from_units(200)

    def test_un_limite_menor_a_una_unidad_queda_en_cero(self):
        assert to_units_floor(999_999) == 0
