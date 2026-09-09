"""Pruebas del motor de scoring.

Tres propiedades por encima de la precisión del modelo: que la decisión sea
explicable, reproducible y no discriminatoria. Un modelo que acierta pero no puede
justificarse ante un supervisor no se puede poner en producción.
"""

from __future__ import annotations

from datetime import date, datetime, timedelta

import pytest

from aibank_risk import (
    MODEL_VERSION,
    Direction,
    Movement,
    Outcome,
    ProhibitedFeatureError,
    assess,
    extract,
    is_prohibited,
    score,
)
from aibank_risk.fairness import assert_permitted
from aibank_risk.policy import MAX_INCOME_SHARE, MAX_INITIAL_LIMIT_MINOR

AS_OF = date(2026, 9, 1)
EVALUATED_AT = datetime(2026, 9, 1, 10, 30)


def salaried(months: int = 8, monthly_minor: int = 250_000) -> list[Movement]:
    """Perfil de ingreso recurrente: mismo origen, importe estable, cada mes."""
    movements: list[Movement] = []
    for month in range(months):
        when = datetime.combine(AS_OF, datetime.min.time()) - timedelta(days=30 * month + 2)
        movements.append(
            Movement(when, Direction.IN, monthly_minor, counterparty="empleador-1", kind="payroll")
        )
        # Gasta el 60%: deja margen para una cuota.
        for week in range(4):
            movements.append(
                Movement(
                    when + timedelta(days=week * 6),
                    Direction.OUT,
                    int(monthly_minor * 0.15),
                    counterparty=f"comercio-{week}",
                    kind="p2p_transfer",
                )
            )
    return movements


def irregular(months: int = 8) -> list[Movement]:
    """Ingreso informal: entra dinero, pero de orígenes y montos dispares."""
    amounts = [40_000, 210_000, 15_000, 90_000, 5_000, 160_000, 30_000, 70_000]
    movements: list[Movement] = []
    for month in range(months):
        when = datetime.combine(AS_OF, datetime.min.time()) - timedelta(days=30 * month + 3)
        movements.append(
            Movement(
                when,
                Direction.IN,
                amounts[month % len(amounts)],
                counterparty=f"cliente-{month}",
                kind="rail_inbound_credit",
            )
        )
        movements.append(
            Movement(
                when + timedelta(days=2),
                Direction.OUT,
                int(amounts[month % len(amounts)] * 0.95),
                counterparty="retiro",
                kind="withdrawal",
            )
        )
    return movements


# ---------------------------------------------------------------- características


class TestCaracteristicas:
    def test_detecta_un_ingreso_recurrente(self) -> None:
        features = extract(salaried(), as_of=AS_OF)

        assert features.has_recurring_income, (
            "un mismo origen pagando un importe estable cada mes es una nómina "
            "aunque nadie la haya declarado"
        )
        assert features.inflow_regularity > 0.8

    def test_un_ingreso_disperso_no_pasa_por_recurrente(self) -> None:
        features = extract(irregular(), as_of=AS_OF)

        assert not features.has_recurring_income
        assert features.inflow_regularity < 0.5

    def test_los_meses_sin_ingreso_bajan_la_regularidad(self) -> None:
        # Cobró una sola vez y desapareció.
        una_vez = [
            Movement(
                datetime.combine(AS_OF, datetime.min.time()) - timedelta(days=20),
                Direction.IN,
                500_000,
                counterparty="cliente",
                kind="deposit",
            )
        ]
        features = extract(una_vez, as_of=AS_OF)

        assert features.inflow_regularity == 0.0, (
            "un solo ingreso alto no puede parecerse a cobrar todos los meses"
        )

    def test_una_cuenta_sin_movimientos_no_rompe_el_calculo(self) -> None:
        features = extract([], as_of=AS_OF)

        assert features.months_of_history == 0
        assert features.average_monthly_inflow_minor == 0
        assert features.outflow_to_inflow_ratio == 0.0

    def test_el_calculo_es_reproducible(self) -> None:
        movements = salaried()
        assert extract(movements, as_of=AS_OF) == extract(movements, as_of=AS_OF)


# ---------------------------------------------------------------- puntaje


class TestPuntaje:
    def test_un_perfil_con_nomina_puntua_mas_que_uno_informal(self) -> None:
        con_nomina = score(extract(salaried(), as_of=AS_OF))
        informal = score(extract(irregular(), as_of=AS_OF))

        assert con_nomina.value > informal.value

    def test_toda_decision_trae_su_version_de_modelo(self) -> None:
        # Sin versión no se puede reconstruir con qué reglas se resolvió un caso.
        assert score(extract(salaried(), as_of=AS_OF)).model_version == MODEL_VERSION

    def test_el_puntaje_es_reproducible(self) -> None:
        features = extract(salaried(), as_of=AS_OF)
        assert score(features).value == score(features).value

    def test_el_puntaje_se_mantiene_en_su_rango(self) -> None:
        for movements in ([], salaried(), irregular(), salaried(months=24, monthly_minor=5_000_000)):
            value = score(extract(movements, as_of=AS_OF)).value
            assert 0 <= value <= 1000

    def test_los_rechazos_por_falta_de_fondos_penalizan(self) -> None:
        limpio = score(extract(salaried(), as_of=AS_OF))
        con_rechazos = score(
            extract(salaried(), as_of=AS_OF, insufficient_funds_events=4)
        )

        assert con_rechazos.value < limpio.value

    def test_los_motivos_explican_que_falto(self) -> None:
        resultado = score(extract(irregular(), as_of=AS_OF))
        motivos = resultado.reason_codes()

        assert motivos, "una negativa sin motivos no se le puede comunicar a nadie"
        assert all(isinstance(m, str) and m for m in motivos)
        # Deben ser frases dirigidas a la persona, no nombres de variables.
        assert all("_" not in m for m in motivos)

    def test_los_motivos_se_ordenan_por_lo_que_mas_resto(self) -> None:
        resultado = score(extract([], as_of=AS_OF))
        motivos = resultado.reason_codes(limit=2)

        # Sin ingreso recurrente (200 puntos) pesa más que la falta de arraigo (60).
        assert "ingreso que se repita" in motivos[0]


# ---------------------------------------------------------------- decisión


class TestDecision:
    def test_un_perfil_con_nomina_estable_es_aprobado(self) -> None:
        resultado = assess("cli-1", salaried(), as_of=AS_OF, evaluated_at=EVALUATED_AT)

        assert resultado.decision.outcome is Outcome.APPROVED
        assert resultado.decision.limit_minor > 0
        assert resultado.decision.reasons == (), "una aprobación no necesita motivos"

    def test_una_cuenta_nueva_no_se_rechaza_sino_que_se_pospone(self) -> None:
        reciente = [
            Movement(
                datetime.combine(AS_OF, datetime.min.time()) - timedelta(days=5),
                Direction.IN,
                120_000,
                counterparty="empleador-1",
                kind="payroll",
            )
        ]
        resultado = assess("cli-2", reciente, as_of=AS_OF, evaluated_at=EVALUATED_AT)

        assert resultado.decision.outcome is Outcome.NEEDS_MORE_HISTORY, (
            "ser nuevo no es ser mal pagador: se pide más historial, no se rechaza"
        )
        assert resultado.decision.limit_minor == 0
        assert resultado.decision.reasons

    def test_una_cuenta_vacia_no_se_rechaza_por_falta_de_datos(self) -> None:
        resultado = assess("cli-3", [], as_of=AS_OF, evaluated_at=EVALUATED_AT)

        # No hay evidencia MALA: no hay evidencia. Son cosas distintas y solo una
        # justifica dejarle a alguien un rechazo en su historial.
        assert resultado.decision.outcome is Outcome.NEEDS_MORE_HISTORY
        assert resultado.decision.limit_minor == 0
        assert resultado.decision.reasons

    def test_un_rechazo_exige_evidencia_de_comportamiento(self) -> None:
        # Ocho meses observados, ingreso errático, gasta casi todo, cuenta seca y
        # pagos rechazados por falta de fondos: aquí sí hay con qué decir que no.
        resultado = assess(
            "cli-4",
            irregular(),
            as_of=AS_OF,
            evaluated_at=EVALUATED_AT,
            zero_balance_days=120,
            insufficient_funds_events=5,
        )

        assert resultado.decision.outcome is Outcome.DECLINED
        assert resultado.decision.reasons

    # La restricción que impide prestar contra un ingreso que no existe.
    def test_el_limite_nunca_supera_la_proporcion_del_ingreso(self) -> None:
        for monthly in (100_000, 250_000, 400_000, 900_000):
            resultado = assess(
                "cli-x",
                salaried(monthly_minor=monthly),
                as_of=AS_OF,
                evaluated_at=EVALUATED_AT,
            )
            tope = int(resultado.features.average_monthly_inflow_minor * MAX_INCOME_SHARE)
            assert resultado.decision.limit_minor <= tope, (
                f"con ingreso {monthly} el límite {resultado.decision.limit_minor} "
                f"supera el {int(MAX_INCOME_SHARE * 100)}% del ingreso observado"
            )

    def test_ningun_limite_inicial_supera_el_techo(self) -> None:
        resultado = assess(
            "cli-rico",
            salaried(months=36, monthly_minor=20_000_000),
            as_of=AS_OF,
            evaluated_at=EVALUATED_AT,
        )
        assert resultado.decision.limit_minor <= MAX_INITIAL_LIMIT_MINOR, (
            "nadie estrena una línea grande: primero hay que ver cómo paga"
        )

    def test_mas_antiguedad_no_reduce_el_limite(self) -> None:
        limites = [
            assess(
                "cli",
                salaried(months=meses),
                as_of=AS_OF,
                evaluated_at=EVALUATED_AT,
            ).decision.limit_minor
            for meses in (3, 6, 12)
        ]
        assert limites == sorted(limites), f"el límite debería crecer con la relación: {limites}"

    def test_la_evaluacion_es_reproducible(self) -> None:
        movements = salaried()
        primera = assess("cli-1", movements, as_of=AS_OF, evaluated_at=EVALUATED_AT)
        segunda = assess("cli-1", movements, as_of=AS_OF, evaluated_at=EVALUATED_AT)

        assert primera.to_json() == segunda.to_json(), (
            "una decisión de crédito tiene que poder recalcularse igual años después"
        )


# ---------------------------------------------------------------- expediente


class TestExpediente:
    def test_el_expediente_permite_reconstruir_la_decision(self) -> None:
        registro = assess(
            "cli-1", salaried(), as_of=AS_OF, evaluated_at=EVALUATED_AT
        ).to_record()

        for clave in ("score", "model_version", "features", "contributions", "as_of"):
            assert clave in registro, f"sin '{clave}' la decisión no es auditable"
        assert registro["model_version"] == MODEL_VERSION

    def test_el_expediente_no_guarda_datos_personales(self) -> None:
        registro = assess("cli-1", salaried(), as_of=AS_OF, evaluated_at=EVALUATED_AT).to_record()
        texto = str(registro).lower()

        # Un expediente de crédito se conserva años y lo consultan varios sistemas.
        for prohibido in ("document", "email", "phone", "name", "birth"):
            assert prohibido not in texto, f"el expediente filtra '{prohibido}'"


# ---------------------------------------------------------------- no discriminación


class TestNoDiscriminacion:
    @pytest.mark.parametrize(
        "atributo",
        [
            "gender",
            "applicant_sex",
            "race",
            "religion",
            "marital_status",
            "date_of_birth",
            "age_band",
            "nationality",
            "postal_code",
            "zip_code_bucket",
            "neighborhood",
            "last_name",
            "selfie_score",
        ],
    )
    def test_los_atributos_protegidos_y_sus_sustitutos_estan_prohibidos(
        self, atributo: str
    ) -> None:
        assert is_prohibited(atributo)

    @pytest.mark.parametrize(
        "atributo",
        [
            "months_of_history",
            "average_monthly_inflow_minor",
            "inflow_regularity",
            "has_recurring_income",
            "outflow_to_inflow_ratio",
            "zero_balance_ratio",
            "distinct_counterparties",
            "insufficient_funds_events",
        ],
    )
    def test_el_comportamiento_observado_si_puede_usarse(self, atributo: str) -> None:
        assert not is_prohibited(atributo), (
            "prestarle a quien no tiene buró exige poder usar su comportamiento"
        )

    def test_usar_un_atributo_protegido_falla(self) -> None:
        with pytest.raises(ProhibitedFeatureError):
            assert_permitted(["months_of_history", "gender"])

    def test_ninguna_caracteristica_del_modelo_es_un_atributo_protegido(self) -> None:
        # La barrera corre dentro de assess(), así que esto vale como prueba de
        # que una característica añadida más adelante no puede colarse.
        assess("cli-1", salaried(), as_of=AS_OF, evaluated_at=EVALUATED_AT)
