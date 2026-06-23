#!/bin/bash
#
# uninstall.sh — Remove monitor-network-rtmp exporter khỏi Ubuntu.
#
# Usage: sudo bash uninstall.sh
#
set -euo pipefail

[[ "$EUID" -eq 0 ]] || { echo "Please run as root: sudo bash uninstall.sh"; exit 1; }

SERVICE_NAME="monitor-network-rtmp"
BINARY_PATH="/usr/local/bin/monitor-network-rtmp"
UNIT_PATH="/etc/systemd/system/${SERVICE_NAME}.service"

echo "[1/4] Stop & disable service..."
systemctl stop "${SERVICE_NAME}.service" 2>/dev/null || true
systemctl disable "${SERVICE_NAME}.service" 2>/dev/null || true

echo "[2/4] Remove systemd unit..."
rm -f "$UNIT_PATH"
systemctl daemon-reload

echo "[3/4] Remove binary..."
rm -f "$BINARY_PATH"

echo "[4/4] Remove override (if any)..."
rm -rf "/etc/systemd/system/${SERVICE_NAME}.service.d"

echo ""
echo "Done. monitor-network-rtmp has been removed."
