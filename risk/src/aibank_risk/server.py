"""Servicio HTTP del motor de riesgo.

El motor es una biblioteca; el banco lo consulta por aquí. Una sola operación:
evaluar a un cliente con los movimientos que el banco le pasa, y devolver el
expediente completo de la decisión.

Solo biblioteca estándar, como el resto del paquete: lo que decide el crédito de
alguien tiene que ser pequeño, auditable y sin dependencias que revisar.

    python -m aibank_risk.server --port 8090
"""

from __future__ import annotations

import argparse
import json
import logging
from datetime import UTC, date, datetime
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any

from .assessment import assess
from .features import Direction, Movement
from .income import next_income

log = logging.getLogger("aibank_risk.server")

#: Tope del cuerpo de una petición. Seis meses de movimientos de una cuenta
#: personal caben de sobra; algo mayor es un error o un abuso.
MAX_BODY_BYTES = 2_000_000


class InvalidRequest(ValueError):
    """La petición no describe una evaluación válida."""


def _instant(value: str) -> datetime:
    """Un instante en UTC y sin zona, que es como razona el motor.

    El banco manda instantes con zona (`2026-10-27T12:48:52Z`); los cálculos del
    motor comparan fechas sin ella. Mezclarlas no es un detalle: Python se niega
    a compararlas, y una decisión de crédito no puede depender de en qué zona
    venía escrita una fecha.
    """
    parsed = datetime.fromisoformat(value)
    if parsed.tzinfo is not None:
        parsed = parsed.astimezone(UTC).replace(tzinfo=None)
    return parsed


def _movements(body: dict[str, Any]) -> list[Movement]:
    """Los movimientos de una petición, en el formato del motor."""
    return [
        Movement(
            occurred_at=_instant(m["occurred_at"]),
            direction=Direction(m["direction"]),
            amount_micros=int(m["amount_micros"]),
            counterparty=str(m.get("counterparty", "")),
            kind=str(m.get("kind", "")),
        )
        for m in body.get("movements", [])
    ]


def parse_request(body: dict[str, Any]) -> dict[str, Any]:
    """Valida y convierte el JSON de una petición en los argumentos de `assess`."""
    try:
        customer_id = str(body["customer_id"])
        as_of = date.fromisoformat(body["as_of"])
        evaluated_at = _instant(body["evaluated_at"])
        movements = _movements(body)
    except (KeyError, TypeError, ValueError) as e:
        raise InvalidRequest(f"petición inválida: {e}") from e
    if not customer_id:
        raise InvalidRequest("falta customer_id")
    return {
        "customer_id": customer_id,
        "movements": movements,
        "as_of": as_of,
        "evaluated_at": evaluated_at,
        "currency": str(body.get("currency", "USD")),
        "observed_days": int(body.get("observed_days", 180)),
    }


class Handler(BaseHTTPRequestHandler):
    server_version = "aibank-risk"

    def do_GET(self) -> None:  # noqa: N802 — nombre que exige http.server
        if self.path == "/health":
            self._reply(200, {"status": "ok"})
        else:
            self._reply(404, {"code": "not_found", "message": "no encontrado"})

    def do_POST(self) -> None:  # noqa: N802
        if self.path == "/v1/income":
            self._income()
            return
        if self.path != "/v1/assessments":
            self._reply(404, {"code": "not_found", "message": "no encontrado"})
            return
        length = int(self.headers.get("Content-Length") or 0)
        if length <= 0 or length > MAX_BODY_BYTES:
            self._reply(400, {"code": "invalid_request", "message": "cuerpo vacío o demasiado grande"})
            return
        try:
            body = json.loads(self.rfile.read(length))
            if not isinstance(body, dict):
                raise InvalidRequest("el cuerpo debe ser un objeto JSON")
            assessment = assess(**parse_request(body))
        except (json.JSONDecodeError, InvalidRequest) as e:
            self._reply(400, {"code": "invalid_request", "message": str(e)})
            return
        except Exception:
            # Nunca cortar la conexión sin respuesta: el banco tiene que saber que
            # el motor falló, y el detalle queda aquí.
            log.exception("fallo inesperado al evaluar")
            self._reply(500, {"code": "internal", "message": "fallo del motor al evaluar"})
            return
        record = assessment.to_record()
        log.info(
            "evaluación cliente=%s resultado=%s puntaje=%s modelo=%s",
            record["customer_id"], record["outcome"], record["score"], record["model_version"],
        )
        self._reply(200, record)

    def _income(self) -> None:
        """El próximo ingreso esperado, para la previsión de saldo.

        No decide nada ni se archiva: es una lectura de los movimientos, con el
        mismo criterio de ingreso recurrente que la evaluación.
        """
        length = int(self.headers.get("Content-Length") or 0)
        if length <= 0 or length > MAX_BODY_BYTES:
            self._reply(400, {"code": "invalid_request", "message": "cuerpo vacío o demasiado grande"})
            return
        try:
            body = json.loads(self.rfile.read(length))
            if not isinstance(body, dict):
                raise InvalidRequest("el cuerpo debe ser un objeto JSON")
            as_of = date.fromisoformat(body["as_of"])
            movements = _movements(body)
        except (json.JSONDecodeError, InvalidRequest, KeyError, TypeError, ValueError) as e:
            self._reply(400, {"code": "invalid_request", "message": f"petición inválida: {e}"})
            return
        income = next_income(movements, as_of=as_of)
        self._reply(200, {"recurring_income": income.to_record() if income else None})

    def _reply(self, status: int, payload: dict[str, Any]) -> None:
        raw = json.dumps(payload, ensure_ascii=False).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def log_message(self, format: str, *args: Any) -> None:  # noqa: A002
        # El acceso va al logger del módulo, no a stderr sin formato.
        log.debug(format, *args)


def serve(host: str, port: int) -> ThreadingHTTPServer:
    return ThreadingHTTPServer((host, port), Handler)


def main() -> None:
    parser = argparse.ArgumentParser(description="Servicio HTTP del motor de riesgo")
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=8090)
    args = parser.parse_args()
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
    server = serve(args.host, args.port)
    log.info("motor de riesgo escuchando en %s:%s", args.host, args.port)
    server.serve_forever()


if __name__ == "__main__":
    main()
