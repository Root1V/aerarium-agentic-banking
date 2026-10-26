#!/usr/bin/env bash
# Levanta el sandbox y arranca la app contra él, en un comando.
#
#   app/tool/sandbox.sh ios    # simulador de iPhone, con hot reload
#   app/tool/sandbox.sh web    # portal web en Chrome, con hot reload
#
# Por qué existe: probar la app a mano exige que cinco valores coincidan entre
# sí —el puerto del canal del titular, el CORS para el origen del portal, la
# clave del simulador, la URL que se compila en la app y el modo sandbox—, y
# basta con que uno no case para ver un error que no explica nada.
#
# Puertos por defecto 38080/38081/5050, y no 8080/8081: son los que media docena
# de herramientas usan por defecto, y en una máquina de desarrollo casi siempre
# hay alguna ocupándolos. Se cambian con HOLDER_PORT, PARTNER_PORT y WEB_PORT.
set -euo pipefail

mode="${1:-}"
if [[ "$mode" != "ios" && "$mode" != "web" ]]; then
  echo "uso: $0 ios|web" >&2
  exit 2
fi

root="$(cd "$(dirname "$0")/../.." && pwd)"
holder_port="${HOLDER_PORT:-38080}"
partner_port="${PARTNER_PORT:-38081}"
web_port="${WEB_PORT:-5050}"
env_file="$root/platform/.env"
compose=(docker compose -p aerarium-app -f "$root/platform/docker-compose.sandbox.yml")

# Sin clave de firma el sandbox no arranca, a propósito. La primera vez se crea.
if ! grep -q '^OAUTH_SIGNING_KEY=' "$env_file" 2>/dev/null; then
  echo "OAUTH_SIGNING_KEY=$(head -c 32 /dev/urandom | base64)" >> "$env_file"
  echo "· creada la clave de firma en platform/.env"
fi

echo "· levantando el sandbox (titular :$holder_port, socio :$partner_port)"
PARTNER_PUBLISH="127.0.0.1:$partner_port" \
HOLDER_PUBLISH="127.0.0.1:$holder_port" \
HOLDER_CORS_ORIGINS="http://localhost:$web_port" \
  "${compose[@]}" up --build -d >/dev/null

for _ in $(seq 1 60); do
  curl -sf "http://localhost:$holder_port/health" >/dev/null && break
  sleep 2
done
if ! curl -sf "http://localhost:$holder_port/health" >/dev/null; then
  echo "el canal del titular no responde en :$holder_port; mira: ${compose[*]} logs holder" >&2
  exit 1
fi

# La clave del simulador de titulares, si el entorno la exige. Se lee del mismo
# archivo que usa Docker para que nunca puedan discrepar.
dev_key="$(grep '^DEV_API_KEY=' "$env_file" 2>/dev/null | cut -d= -f2- || true)"

defines=(
  --dart-define=AIBANK_SANDBOX=true
  --dart-define=AIBANK_BFF_URL="http://localhost:$holder_port"
  --dart-define=AIBANK_DEV_KEY="$dev_key"
)

cd "$root/app"

if [[ "$mode" == "web" ]]; then
  echo "· abriendo el portal en http://localhost:$web_port"
  exec flutter run -d chrome --web-port="$web_port" "${defines[@]}"
fi

# Un simulador de iPhone arrancado; si no hay ninguno, el primero disponible.
udid="$(xcrun simctl list devices booted | grep -m1 -oE 'iPhone.*\(([0-9A-F-]{36})\)' | grep -oE '[0-9A-F-]{36}' || true)"
if [[ -z "$udid" ]]; then
  udid="$(xcrun simctl list devices available | grep -m1 -oE 'iPhone.*\(([0-9A-F-]{36})\)' | grep -oE '[0-9A-F-]{36}')"
  echo "· arrancando el simulador"
  xcrun simctl boot "$udid"
fi
open -a Simulator
echo "· compilando para el simulador (la primera vez tarda un par de minutos)"
exec flutter run -d "$udid" "${defines[@]}"
