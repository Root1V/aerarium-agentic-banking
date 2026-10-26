#!/usr/bin/env bash
# Instala la app en un iPhone conectado por cable y la apunta al sandbox de este
# Mac, por la Wi-Fi.
#
#   app/tool/iphone.sh
#
# A diferencia de sandbox.sh, la app NO se compila en modo sandbox: arranca como
# la de verdad. Abres una cuenta con tu DNI (KYC simulado), proteges la cuenta con
# tu clave y Face ID, y desde ahí entras con la cara. Lo único de prueba es el
# banco detrás.
#
# Se compila en modo release: una app de depuración de Flutter no se puede volver
# a abrir desde la pantalla de inicio una vez desconectado el cable, y probar
# "cierro la app y vuelvo a entrar con Face ID" es justamente lo que importa.
#
# Requisitos del iPhone, una vez: Modo desarrollador activado (Ajustes ›
# Privacidad y seguridad), confiar en este Mac al conectarlo, y la misma Wi-Fi
# que el Mac.
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
holder_port="${HOLDER_PORT:-38080}"
partner_port="${PARTNER_PORT:-38081}"
env_file="$root/platform/.env"
compose=(docker compose -p aerarium-app -f "$root/platform/docker-compose.sandbox.yml")

# La IP del Mac en la Wi-Fi: es a donde va a llamar el iPhone.
ip="${MAC_IP:-$(ipconfig getifaddr en0 || ipconfig getifaddr en1 || true)}"
if [[ -z "$ip" ]]; then
  echo "no encuentro la IP del Mac en la Wi-Fi; pásala con MAC_IP=..." >&2
  exit 1
fi

if ! grep -q '^OAUTH_SIGNING_KEY=' "$env_file" 2>/dev/null; then
  echo "OAUTH_SIGNING_KEY=$(head -c 32 /dev/urandom | base64)" >> "$env_file"
fi
# El canal del titular queda abierto a la red local, y con él el simulador de
# titulares (/dev), que emite sesiones para cualquiera. Con una clave, nadie más
# en la Wi-Fi puede usarlo. La app del iPhone no la necesita: no usa /dev.
if ! grep -q '^DEV_API_KEY=.' "$env_file" 2>/dev/null; then
  sed -i '' '/^DEV_API_KEY=/d' "$env_file" 2>/dev/null || true
  echo "DEV_API_KEY=$(head -c 24 /dev/urandom | base64 | tr -d '/+=')" >> "$env_file"
  echo "· creada DEV_API_KEY en platform/.env (el canal queda expuesto a la red local)"
fi

echo "· levantando el sandbox, canal del titular en http://$ip:$holder_port"
PARTNER_PUBLISH="127.0.0.1:$partner_port" \
HOLDER_PUBLISH="0.0.0.0:$holder_port" \
  "${compose[@]}" --env-file "$env_file" up --build -d >/dev/null

for _ in $(seq 1 60); do
  curl -sf "http://$ip:$holder_port/health" >/dev/null && break
  sleep 2
done
if ! curl -sf "http://$ip:$holder_port/health" >/dev/null; then
  echo "el canal no responde en http://$ip:$holder_port; mira: ${compose[*]} logs holder" >&2
  exit 1
fi

cd "$root/app"
device="$(flutter devices --machine 2>/dev/null | python3 -c '
import json, sys
for d in json.load(sys.stdin):
    if d.get("targetPlatform", "").startswith("ios") and not d.get("emulator"):
        print(d["id"]); break
')"
if [[ -z "$device" ]]; then
  echo "no veo ningún iPhone. Conéctalo por cable, desbloquéalo y acepta «Confiar en este Mac»." >&2
  exit 1
fi

echo "· instalando en el iPhone ($device). La primera vez tarda unos minutos."
exec flutter run --release -d "$device" --dart-define=AIBANK_BFF_URL="http://$ip:$holder_port"
