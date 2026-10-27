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
# Requisitos, una vez: en el iPhone, Modo desarrollador activado (Ajustes ›
# Privacidad y seguridad), confiar en este Mac al conectarlo, y la misma Wi-Fi
# que el Mac. En el Mac, un Apple ID en Xcode › Settings › Accounts (gratis
# basta); con uno gratis, la primera vez hay que confiar en el desarrollador en
# Ajustes › General › VPN y gestión de dispositivos.
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
# Un iPhone físico. Con varios dispositivos Apple conectados —un iPad por la Wi-Fi,
# por ejemplo— se elige el iPhone; DEVICE_ID fuerza uno concreto.
device="${DEVICE_ID:-$(flutter devices --machine 2>/dev/null | python3 -c '
import json, sys
phones = [d for d in json.load(sys.stdin)
          if d.get("targetPlatform", "").startswith("ios") and not d.get("emulator")]
phones.sort(key=lambda d: "iphone" not in d.get("name", "").lower())
if phones:
    print(phones[0]["id"])
')}"
if [[ -z "$device" ]]; then
  echo "no veo ningún iPhone. Conéctalo por cable, desbloquéalo y acepta «Confiar en este Mac»." >&2
  exit 1
fi

# Se compila con xcodebuild y se instala con devicectl, no con `flutter run`: con
# iOS 26 Flutter a veces cree que el Modo desarrollador está apagado cuando no lo
# está, y Xcode es quien crea el perfil de firma con la cuenta de Xcode › Settings
# › Accounts (-allowProvisioningUpdates).
echo "· compilando y firmando para el iPhone ($device). La primera vez tarda unos minutos."
flutter build ios --config-only --release --dart-define=AIBANK_BFF_URL="http://$ip:$holder_port" >/dev/null
if ! xcodebuild -workspace ios/Runner.xcworkspace -scheme Runner -configuration Release \
     -destination "id=$device" -allowProvisioningUpdates -allowProvisioningDeviceRegistration \
     build >/tmp/aerarium-iphone-build.log 2>&1; then
  grep -E "error:" /tmp/aerarium-iphone-build.log | head -5 >&2
  echo "la compilación falló; si dice 'No Accounts', añade tu Apple ID en Xcode › Settings › Accounts" >&2
  exit 1
fi
app="$(xcodebuild -workspace ios/Runner.xcworkspace -scheme Runner -configuration Release -showBuildSettings 2>/dev/null |
  awk -F' = ' '/ BUILT_PRODUCTS_DIR =/{d=$2} / WRAPPER_NAME =/{w=$2} END{print d"/"w}')"

echo "· instalando"
xcrun devicectl device install app --device "$device" "$app" >/dev/null
if ! xcrun devicectl device process launch --device "$device" dev.aerarium.aibankApp >/dev/null 2>&1; then
  echo "instalada. Si no abre: Ajustes › General › VPN y gestión de dispositivos › tu Apple ID › Confiar."
  exit 0
fi
echo "· listo: AIBank abierta en el iPhone, contra http://$ip:$holder_port"
