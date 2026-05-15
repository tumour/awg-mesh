#!/bin/bash
# install.sh — установка meshd на хост.
#
# Принимает путь к локальному бинарнику или скачивает с GitHub Release.
# Создаёт systemd-юнит, /etc/meshd дирректорию (chmod 700) и regen prerequisites.
#
# Запускать: bash install.sh [--binary <path>] [--version <tag>]
# Без аргументов — пытается скачать latest release.

set -euo pipefail

PREFIX="${PREFIX:-/usr/local}"
SYSTEMD_DIR="${SYSTEMD_DIR:-/etc/systemd/system}"
STATE_DIR="${STATE_DIR:-/etc/meshd}"
GH_REPO="${GH_REPO:-tumour/awg-mesh}"

BINARY=""
VERSION="latest"

while [[ $# -gt 0 ]]; do
    case "$1" in
        --binary) BINARY="$2"; shift 2;;
        --version) VERSION="$2"; shift 2;;
        -h|--help)
            cat <<EOF
Usage: $0 [--binary /path/to/meshd] [--version v0.1.0]
       $0  # download latest from GitHub Release

Environment overrides:
  PREFIX        install prefix (default /usr/local)
  SYSTEMD_DIR   systemd unit dir (default /etc/systemd/system)
  STATE_DIR     state directory (default /etc/meshd)
  GH_REPO       owner/repo for downloads
EOF
            exit 0;;
        *) echo "unknown arg: $1" >&2; exit 2;;
    esac
done

if [[ $EUID -ne 0 ]]; then
    echo "must run as root (or via sudo)" >&2
    exit 1
fi

ARCH="$(uname -m)"
case "$ARCH" in
    x86_64)  GO_ARCH=amd64;;
    aarch64) GO_ARCH=arm64;;
    armv7l)  GO_ARCH=armv7;;
    *) echo "unsupported arch: $ARCH" >&2; exit 1;;
esac

echo "==> installing meshd ($GO_ARCH) to $PREFIX/bin"

mkdir -p "$PREFIX/bin"
if [[ -n "$BINARY" ]]; then
    [[ -f "$BINARY" ]] || { echo "binary not found: $BINARY" >&2; exit 1; }
    install -m 0755 "$BINARY" "$PREFIX/bin/meshd"
else
    url="https://github.com/$GH_REPO/releases/$VERSION/download/meshd-linux-$GO_ARCH"
    if [[ "$VERSION" == "latest" ]]; then
        url="https://github.com/$GH_REPO/releases/latest/download/meshd-linux-$GO_ARCH"
    fi
    echo "==> downloading $url"
    curl -fsSL "$url" -o "$PREFIX/bin/meshd.tmp"
    chmod 755 "$PREFIX/bin/meshd.tmp"
    mv "$PREFIX/bin/meshd.tmp" "$PREFIX/bin/meshd"
fi

# State dir с chmod 700 — приватные ключи и cluster-secret в /etc/meshd/state.json.
mkdir -p "$STATE_DIR"
chmod 700 "$STATE_DIR"

# systemd unit
echo "==> installing systemd unit"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
UNIT_SRC="$SCRIPT_DIR/systemd/meshd.service"
if [[ -f "$UNIT_SRC" ]]; then
    install -m 0644 "$UNIT_SRC" "$SYSTEMD_DIR/meshd.service"
else
    # При установке через curl — unit пишется inline.
    cat > "$SYSTEMD_DIR/meshd.service" <<'EOF'
[Unit]
Description=AmneziaWG mesh node daemon (meshd)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/meshd run
Restart=on-failure
RestartSec=5s
AmbientCapabilities=CAP_NET_ADMIN
CapabilityBoundingSet=CAP_NET_ADMIN
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/etc/meshd
PrivateTmp=true
ProtectKernelTunables=true
ProtectKernelModules=true
StandardOutput=journal
StandardError=journal
SyslogIdentifier=meshd

[Install]
WantedBy=multi-user.target
EOF
fi

systemctl daemon-reload

VERSION_OUT="$("$PREFIX/bin/meshd" version 2>/dev/null || echo unknown)"

cat <<EOF

✓ meshd installed (version: $VERSION_OUT)

  binary:  $PREFIX/bin/meshd
  state:   $STATE_DIR/
  unit:    $SYSTEMD_DIR/meshd.service

Next steps:

  # 1. Initialize this node as seed (only on first node):
  $PREFIX/bin/meshd init --label <name> --public-endpoint <public-ip>:51820

  # ...OR join existing mesh (subsequent nodes):
  $PREFIX/bin/meshd join --label <name> --token <token-from-seed>

  # 2. Open UFW for the bootstrap+wg port (only on the seed):
  ufw allow 51820/tcp
  ufw allow 51820/udp

  # 3. Enable & start the daemon:
  systemctl enable --now meshd

  # 4. Check status:
  $PREFIX/bin/meshd status
  systemctl status meshd
  journalctl -u meshd -f

EOF
