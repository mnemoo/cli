#!/usr/bin/env bash
set -euo pipefail

REPOSITORY="${STAKE_ACCELERATOR_REPOSITORY:-mnemoo/cli}"
PORT=7775
VERSION="${STAKE_ACCELERATOR_VERSION:-latest}"
PASSWORD_FILE=""
BIN_PATH="/usr/local/bin/stake-accelerator"
CONFIG_DIR="/etc/stake-accelerator"
STATE_DIR="/var/lib/stake-accelerator"
SERVICE_FILE="/etc/systemd/system/stake-accelerator.service"

usage() {
  echo "Usage: install-accelerator.sh [--port 7775] [--version vX.Y.Z] [--password-file PATH]"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --port)
      PORT="${2:?--port requires a value}"
      shift 2
      ;;
    --version)
      VERSION="${2:?--version requires a value}"
      shift 2
      ;;
    --password-file)
      PASSWORD_FILE="${2:?--password-file requires a value}"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "Unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

if [[ "$(id -u)" -ne 0 ]]; then
  echo "Run this installer through sudo." >&2
  exit 1
fi
if ! [[ "${PORT}" =~ ^[0-9]+$ ]] || (( PORT < 1 || PORT > 65535 )); then
  echo "Invalid port: ${PORT}" >&2
  exit 1
fi
if [[ "$(uname -s)" != "Linux" ]]; then
  echo "The accelerator daemon currently supports Linux only." >&2
  exit 1
fi
if ! command -v systemctl >/dev/null 2>&1; then
  echo "systemd is required." >&2
  exit 1
fi

case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *)
    echo "Unsupported architecture: $(uname -m)" >&2
    exit 1
    ;;
esac

for command in curl tar sha256sum; do
  if ! command -v "${command}" >/dev/null 2>&1; then
    echo "Required command is missing: ${command}" >&2
    exit 1
  fi
done

if [[ "${VERSION}" == "latest" ]]; then
  VERSION="$(curl -fsSL "https://api.github.com/repos/${REPOSITORY}/releases/latest" |
    sed -n 's/.*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p' |
    head -n 1)"
fi
if [[ -z "${VERSION}" ]]; then
  echo "Could not determine the accelerator release version." >&2
  exit 1
fi
TAG="${VERSION}"
VERSION="${VERSION#v}"
ASSET="stake-accelerator_${VERSION}_linux_${ARCH}.tar.gz"
CHECKSUMS="stakecli_${VERSION}_checksums.txt"
BASE_URL="https://github.com/${REPOSITORY}/releases/download/${TAG}"

TEMP_DIR="$(mktemp -d)"
cleanup() {
  unset PASSWORD PASSWORD_CONFIRM
  rm -rf "${TEMP_DIR}"
}
trap cleanup EXIT

echo "Downloading ${ASSET}..."
curl -fsSL "${BASE_URL}/${ASSET}" -o "${TEMP_DIR}/${ASSET}"
curl -fsSL "${BASE_URL}/${CHECKSUMS}" -o "${TEMP_DIR}/${CHECKSUMS}"
(
  cd "${TEMP_DIR}"
  grep "  ${ASSET}\$" "${CHECKSUMS}" | sha256sum -c -
)
tar -xzf "${TEMP_DIR}/${ASSET}" -C "${TEMP_DIR}" stake-accelerator
install -m 0755 "${TEMP_DIR}/stake-accelerator" "${BIN_PATH}"

if ! getent group stake-accelerator >/dev/null 2>&1; then
  groupadd --system stake-accelerator
fi
if ! id stake-accelerator >/dev/null 2>&1; then
  useradd --system --gid stake-accelerator --home-dir "${STATE_DIR}" \
    --shell /usr/sbin/nologin stake-accelerator
fi
install -d -m 0750 -o root -g stake-accelerator "${CONFIG_DIR}"
install -d -m 0700 -o stake-accelerator -g stake-accelerator "${STATE_DIR}"

if [[ -e "${CONFIG_DIR}/config.json" ]]; then
  echo "Existing accelerator identity preserved: ${CONFIG_DIR}/config.json"
else
  if [[ -n "${PASSWORD_FILE}" ]]; then
    if [[ ! -f "${PASSWORD_FILE}" ]]; then
      echo "Password file does not exist: ${PASSWORD_FILE}" >&2
      exit 1
    fi
    PASSWORD="$(head -n 1 "${PASSWORD_FILE}")"
  else
    IFS= read -r -s -p "Accelerator password: " PASSWORD </dev/tty
    echo >/dev/tty
    IFS= read -r -s -p "Confirm password: " PASSWORD_CONFIRM </dev/tty
    echo >/dev/tty
    if [[ "${PASSWORD}" != "${PASSWORD_CONFIRM}" ]]; then
      echo "Passwords do not match." >&2
      exit 1
    fi
  fi
  if (( ${#PASSWORD} < 12 )); then
    echo "Password must contain at least 12 characters." >&2
    exit 1
  fi
  printf '%s\n' "${PASSWORD}" |
    "${BIN_PATH}" init \
      --config "${CONFIG_DIR}/config.json" \
      --state-dir "${STATE_DIR}" \
      --listen ":${PORT}" \
      --password-stdin
  chown root:stake-accelerator "${CONFIG_DIR}/config.json"
  chmod 0640 "${CONFIG_DIR}/config.json"
  chown -R stake-accelerator:stake-accelerator "${STATE_DIR}"
fi

install -m 0644 /dev/stdin "${SERVICE_FILE}" <<'UNIT'
[Unit]
Description=Stake encrypted upload accelerator
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=stake-accelerator
Group=stake-accelerator
ExecStart=/usr/local/bin/stake-accelerator serve --config /etc/stake-accelerator/config.json
Restart=on-failure
RestartSec=2s
NoNewPrivileges=true
PrivateTmp=true
PrivateDevices=true
ProtectSystem=strict
ProtectHome=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectKernelLogs=true
ProtectControlGroups=true
RestrictSUIDSGID=true
LockPersonality=true
MemoryDenyWriteExecute=true
CapabilityBoundingSet=
AmbientCapabilities=
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
ReadWritePaths=/var/lib/stake-accelerator
UMask=0077

[Install]
WantedBy=multi-user.target
UNIT

systemctl daemon-reload
systemctl enable --now stake-accelerator.service
systemctl --no-pager --full status stake-accelerator.service || true

FINGERPRINT="$("${BIN_PATH}" fingerprint --config "${CONFIG_DIR}/config.json")"
echo
echo "Stake accelerator is running on TCP port ${PORT}."
echo "Certificate ${FINGERPRINT} will be pinned automatically after password authentication."
echo "Make sure TCP/${PORT} is allowed by your cloud firewall when remote access is required."
