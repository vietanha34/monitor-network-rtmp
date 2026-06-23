#!/bin/bash
#
# install.sh — Setup monitor-network-rtmp exporter trên Ubuntu mới hoàn toàn.
#
# Usage:
#   sudo bash install.sh                              # mặc định: port 1935, ss-tcpinfo
#   sudo bash install.sh --port 1935                  # chỉ định port khác
#   sudo bash install.sh --labels env=prod,region=ap  # thêm custom labels
#   sudo bash install.sh --version v0.2.1             # chỉ định version
#
# Chạy với root: sudo bash install.sh
#
set -euo pipefail

# ── Defaults ──────────────────────────────────────────────────────────────────
VERSION="v0.2.1"
TARGET_PORT="1935"
LISTEN_ADDRESS=":9101"
BYTE_SOURCE="ss-tcpinfo"
LABELS=""
INSTALL_DIR="/usr/local/bin"
SERVICE_NAME="monitor-network-rtmp"
REPO_OWNER="vietanha34"

# ── Parse args ────────────────────────────────────────────────────────────────
while [[ $# -gt 0 ]]; do
    case "$1" in
        --port)        TARGET_PORT="$2"; shift 2 ;;
        --listen)      LISTEN_ADDRESS="$2"; shift 2 ;;
        --byte-source) BYTE_SOURCE="$2"; shift 2 ;;
        --labels)      LABELS="$2"; shift 2 ;;
        --version)     VERSION="$2"; shift 2 ;;
        --help|-h)     echo "Usage: sudo bash install.sh [--port 1935] [--listen :9101] [--byte-source ss-tcpinfo] [--labels env=prod,region=ap] [--version v0.2.1]"; exit 0 ;;
        *)             echo "Unknown option: $1"; exit 1 ;;
    esac
done

# ── Helpers ───────────────────────────────────────────────────────────────────
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
info()  { echo -e "${GREEN}[INFO]${NC}  $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC}  $*"; }
error() { echo -e "${RED}[ERROR]${NC} $*"; exit 1; }

# ── Pre-flight checks ─────────────────────────────────────────────────────────
[[ "$EUID" -eq 0 ]] || error "Please run as root: sudo bash install.sh"
command -v wget >/dev/null || error "wget not found. Install: apt install -y wget"
command -v systemctl >/dev/null || error "systemctl not found. This script needs systemd."
command -v ss >/dev/null || error "ss not found. Install: apt install -y iproute2"

info "=== monitor-network-rtmp setup ==="
info "Version:      $VERSION"
info "Target port:  $TARGET_PORT"
info "Listen:       $LISTEN_ADDRESS"
info "Byte source:  $BYTE_SOURCE"
info "Labels:       ${LABELS:-<hostname auto>}"
echo ""

# ── Step 1: Check kernel version (ss-tcpinfo needs >= 4.6) ───────────────────
if [[ "$BYTE_SOURCE" == "ss-tcpinfo" ]] || [[ "$BYTE_SOURCE" == "auto" ]]; then
    KERNEL_MAJOR=$(uname -r | cut -d. -f1)
    KERNEL_MINOR=$(uname -r | cut -d. -f2)
    if [[ "$KERNEL_MAJOR" -lt 4 ]] || ([[ "$KERNEL_MAJOR" -eq 4 ]] && [[ "$KERNEL_MINOR" -lt 6 ]]); then
        warn "Kernel $(uname -r) < 4.6 — ss-tcpinfo may not expose byte counters."
        warn "Falling back to byte-source=conntrack. Install conntrack-tools: apt install -y conntrack-tools"
        BYTE_SOURCE="conntrack"
    fi
fi

# ── Step 2: Download binary ───────────────────────────────────────────────────
info "Step 1/6: Download binary $VERSION"
TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT

ARCH=$(uname -m)
case "$ARCH" in
    x86_64|amd64) BINARY_NAME="monitor-network-rtmp-linux-amd64" ;;
    aarch64|arm64) BINARY_NAME="monitor-network-rtmp-linux-arm64" ;;
    *) error "Unsupported architecture: $ARCH" ;;
esac

BASE_URL="https://github.com/${REPO_OWNER}/monitor-network-rtmp/releases/download/${VERSION}"
info "  Downloading $BINARY_NAME ..."
wget -q -O "$TMPDIR/$BINARY_NAME" "$BASE_URL/$BINARY_NAME" || error "Download failed: $BASE_URL/$BINARY_NAME"
wget -q -O "$TMPDIR/checksums.txt" "$BASE_URL/checksums.txt" || error "Download failed: checksums.txt"

# Verify checksum
info "  Verifying checksum ..."
(cd "$TMPDIR" && sha256sum -c checksums.txt --ignore-missing) || error "Checksum verification failed!"

# ── Step 3: Install binary ────────────────────────────────────────────────────
info "Step 2/6: Install binary to $INSTALL_DIR/"
install -m 0755 "$TMPDIR/$BINARY_NAME" "$INSTALL_DIR/monitor-network-rtmp"
info "  Installed: $INSTALL_DIR/monitor-network-rtmp"

# ── Step 4: Create systemd service ────────────────────────────────────────────
info "Step 3/6: Create systemd service"
cat > "/etc/systemd/system/${SERVICE_NAME}.service" <<EOF
[Unit]
Description=Monitor Network RTMP Prometheus Exporter
Documentation=https://github.com/${REPO_OWNER}/monitor-network-rtmp
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
Group=root
Environment=RTMP_TARGET_PORT=${TARGET_PORT}
Environment=RTMP_LISTEN_ADDRESS=${LISTEN_ADDRESS}
Environment=RTMP_METRICS_PATH=/metrics
Environment=RTMP_BYTE_SOURCE=${BYTE_SOURCE}
Environment=RTMP_LABELS=${LABELS}
ExecStart=${INSTALL_DIR}/monitor-network-rtmp \\
    --target-port=\${RTMP_TARGET_PORT} \\
    --listen-address=\${RTMP_LISTEN_ADDRESS} \\
    --metrics-path=\${RTMP_METRICS_PATH} \\
    --byte-source=\${RTMP_BYTE_SOURCE}
Restart=on-failure
RestartSec=5
NoNewPrivileges=true
ProtectSystem=full
ProtectHome=true

[Install]
WantedBy=multi-user.target
EOF
info "  Created: /etc/systemd/system/${SERVICE_NAME}.service"

# ── Step 5: Enable & start service ────────────────────────────────────────────
info "Step 4/6: Enable & start service"
systemctl daemon-reload
systemctl enable "${SERVICE_NAME}.service"
systemctl restart "${SERVICE_NAME}.service"

# Wait for service to be ready
info "  Waiting for service to start ..."
for i in $(seq 1 10); do
    if curl -s "http://127.0.0.1:${LISTEN_ADDRESS#:}/healthz" | grep -q ok 2>/dev/null; then
        info "  Service is up!"
        break
    fi
    sleep 1
    [[ "$i" -eq 10 ]] && warn "Service didn't respond in 10s — check logs: journalctl -u $SERVICE_NAME -n 20"
done

# ── Step 6: Verify ────────────────────────────────────────────────────────────
info "Step 5/6: Verify service status"
systemctl --no-pager --full status "${SERVICE_NAME}.service" | head -15

info "Step 6/6: Verify metrics output"
echo ""
echo "--- Startup log ---"
journalctl -u "${SERVICE_NAME}" --no-pager -n 5 | grep -E "starting|byte source" || true
echo ""
echo "--- Sample metrics ---"
curl -s "http://127.0.0.1:${LISTEN_ADDRESS#:}/metrics" | grep -E '^netrtmp_(up|byte_source_up|connections_active|scrape_errors_total)' | head -10
echo ""

# ── Done ──────────────────────────────────────────────────────────────────────
HOSTNAME_LABEL=$(curl -s "http://127.0.0.1:${LISTEN_ADDRESS#:}/metrics" | grep '^netrtmp_up{' | grep -o 'hostname="[^"]*"' | head -1)
echo ""
info "=== Setup complete! ==="
info "Metrics endpoint:  http://$(hostname):${LISTEN_ADDRESS#:}/metrics"
info "Health endpoint:   http://$(hostname):${LISTEN_ADDRESS#:}/healthz"
info "Hostname label:    ${HOSTNAME_LABEL:-<not found>}"
info "Service management:"
info "  Status:   sudo systemctl status $SERVICE_NAME"
info "  Restart:  sudo systemctl restart $SERVICE_NAME"
info "  Stop:     sudo systemctl stop $SERVICE_NAME"
info "  Logs:     sudo journalctl -u $SERVICE_NAME -f"
info ""
info "Add to Prometheus scrape_configs:"
echo "  - job_name: 'net-rtmp'"
echo "    scrape_interval: 15s"
echo "    metrics_path: /metrics"
echo "    static_configs:"
echo "      - targets: ['$(hostname -I 2>/dev/null | awk '{print $1}' || echo '<server-ip>'):${LISTEN_ADDRESS#:}']"
