# Motor de riesgo crediticio (risk/): tarjeta de puntuación y política de límites.
#
# Sin dependencias fuera de la biblioteca estándar: la imagen es Python y el
# código, nada más. Lo que decide el crédito de alguien tiene que caber en una
# revisión.
FROM python:3.13-slim

WORKDIR /app
COPY risk/src ./src
ENV PYTHONPATH=/app/src PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1

RUN useradd --system --no-create-home risk
USER risk

EXPOSE 8090
CMD ["python", "-m", "aibank_risk.server", "--host", "0.0.0.0", "--port", "8090"]
