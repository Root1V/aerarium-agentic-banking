"""Pruebas del servicio HTTP: lo que el banco ve del motor."""

from __future__ import annotations

import json
import threading
import urllib.error
import urllib.request
from datetime import datetime, timedelta

import pytest

from aibank_risk.server import serve


@pytest.fixture(scope="module")
def base_url():
    server = serve("127.0.0.1", 0)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    yield f"http://127.0.0.1:{server.server_address[1]}"
    server.shutdown()


def post(base_url: str, body: object) -> tuple[int, dict]:
    request = urllib.request.Request(
        base_url + "/v1/assessments",
        data=json.dumps(body).encode(),
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    try:
        with urllib.request.urlopen(request) as response:
            return response.status, json.loads(response.read())
    except urllib.error.HTTPError as e:
        return e.code, json.loads(e.read())


def salary_history(months: int, amount_micros: int, *, as_of: datetime) -> list[dict]:
    movements = []
    for m in range(months):
        paid = as_of - timedelta(days=30 * m + 2)
        movements.append({"occurred_at": paid.isoformat(), "direction": "in",
                          "amount_micros": amount_micros, "counterparty": "empleador", "kind": "p2p_transfer"})
        movements.append({"occurred_at": (paid + timedelta(days=5)).isoformat(), "direction": "out",
                          "amount_micros": amount_micros // 2, "counterparty": f"tienda-{m}", "kind": "p2p_transfer"})
    return movements


AS_OF = datetime(2026, 9, 1, 10, 0)


def test_una_cuenta_con_seis_meses_de_sueldo_se_aprueba_con_su_expediente(base_url):
    status, record = post(base_url, {
        "customer_id": "c-1", "as_of": "2026-09-01", "evaluated_at": AS_OF.isoformat(),
        "currency": "USD", "movements": salary_history(6, 1_500_000_000, as_of=AS_OF),
    })
    assert status == 200
    assert record["outcome"] == "approved"
    assert record["limit_micros"] > 0
    assert record["model_version"]
    assert record["features"]["months_of_history"] >= 5
    assert "contributions" in record


def test_una_cuenta_nueva_no_se_rechaza_se_pide_mas_historial(base_url):
    status, record = post(base_url, {
        "customer_id": "c-2", "as_of": "2026-09-01", "evaluated_at": AS_OF.isoformat(), "movements": [],
    })
    assert status == 200
    assert record["outcome"] == "needs_more_history"
    assert record["limit_micros"] == 0
    assert record["reasons"], "hay que poder decirle a la persona qué le falta"


def test_la_misma_peticion_da_la_misma_decision(base_url):
    body = {"customer_id": "c-3", "as_of": "2026-09-01", "evaluated_at": AS_OF.isoformat(),
            "movements": salary_history(6, 900_000_000, as_of=AS_OF)}
    assert post(base_url, body) == post(base_url, body)


@pytest.mark.parametrize("body", [
    {},
    {"customer_id": "c", "as_of": "ayer", "evaluated_at": AS_OF.isoformat()},
    {"customer_id": "c", "as_of": "2026-09-01", "evaluated_at": AS_OF.isoformat(),
     "movements": [{"occurred_at": AS_OF.isoformat(), "direction": "in", "amount_micros": -5}]},
    {"customer_id": "c", "as_of": "2026-09-01", "evaluated_at": AS_OF.isoformat(),
     "movements": [{"occurred_at": AS_OF.isoformat(), "direction": "lateral", "amount_micros": 5}]},
])
def test_una_peticion_invalida_se_rechaza_con_motivo(base_url, body):
    status, record = post(base_url, body)
    assert status == 400
    assert record["code"] == "invalid_request"


def test_los_instantes_con_zona_se_entienden_igual_que_sin_ella(base_url):
    """El banco (Go) manda instantes en UTC con zona: la decisión tiene que ser la
    misma que con los mismos instantes sin zona."""
    naive = salary_history(6, 1_200_000_000, as_of=AS_OF)
    aware = [{**m, "occurred_at": m["occurred_at"] + "Z"} for m in naive]
    base = {"customer_id": "c-4", "as_of": "2026-09-01", "currency": "USD"}
    status_a, a = post(base_url, {**base, "evaluated_at": AS_OF.isoformat(), "movements": naive})
    status_b, b = post(base_url, {**base, "evaluated_at": AS_OF.isoformat() + "Z", "movements": aware})
    assert (status_a, status_b) == (200, 200)
    assert a["outcome"] == b["outcome"] == "approved"
    assert a["limit_micros"] == b["limit_micros"]
