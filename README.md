# monitor-network-rtmp

A Prometheus exporter that monitors **outbound TCP connections** (default port
`1935`/RTMP, configurable) from an Ubuntu host to remote servers, and the
**bytes/packets sent and received** over those connections.

It exposes a standard `/metrics` HTTP endpoint so you can scrape it with
Prometheus (or VictoriaMetrics, Grafana Agent, etc.) just like `node_exporter`.

---

## How it works

On every Prometheus scrape, the exporter runs two commands concurrently:

| Source | Command | Purpose |
|--------|---------|---------|
| `ss` (iproute2, built into Ubuntu) | `ss -H -t -n state established` | List established outbound TCP connections whose **peer port** = target port |
| `conntrack` (conntrack-tools) | `conntrack -L -p tcp --dport <PORT>` | Per-flow byte/packet counters for both directions |

It then matches `conntrack` flows to `ss` connections by 4-tuple
`(local_ip, local_port, dest_ip, dest_port)` and exposes:

- **Aggregate** connection count and a **monotonic** total-bytes counter per
  `dest_ip`.
- **Per-connection** current byte/packet counters (gauge) so you can see each
  individual RTMP stream.

### Why an accumulator?

`conntrack` per-flow counters **reset to 0 whenever a flow closes**. A raw sum
is therefore not a monotonic Prometheus counter. The exporter keeps an internal
per-flow accumulator and emits only the **delta** to `netrtmp_bytes_total`, so
that metric is a proper ever-increasing counter safe for `rate()` — even across
connection churn and counter resets.

---

## Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `netrtmp_up` | gauge | — | `1` if the `ss` scrape succeeded, else `0` |
| `netrtmp_conntrack_up` | gauge | — | `1` if the `conntrack` command succeeded, else `0` |
| `netrtmp_connections_active` | gauge | `dest_ip`, `dest_port` | Number of established outbound connections to the target port |
| `netrtmp_bytes_total` | counter | `dest_ip`, `dest_port`, `direction` | Total bytes transferred (monotonic; `direction` = `sent` or `received`) |
| `netrtmp_connection_bytes` | gauge | `dest_ip`, `dest_port`, `local_port`, `direction` | Current conntrack byte counter for a single open connection |
| `netrtmp_connection_packets` | gauge | `dest_ip`, `dest_port`, `local_port`, `direction` | Current conntrack packet counter for a single open connection |
| `netrtmp_scrape_duration_seconds` | gauge | — | Duration of the last scrape |
| `netrtmp_scrape_errors_total` | counter | `source` | Total scrape errors (`source` = `ss` or `conntrack`) |

Stale per-connection series are automatically removed when the connection
closes (cardinality stays bounded to currently-open connections).

### Example output

```
# HELP netrtmp_connections_active Number of established outbound TCP connections to the target port.
netrtmp_connections_active{dest_ip="93.184.216.34",dest_port="1935"} 2
# HELP netrtmp_bytes_total Total bytes transferred over established outbound connections ...
netrtmp_bytes_total{dest_ip="93.184.216.34",dest_port="1935",direction="received"} 34000
netrtmp_bytes_total{dest_ip="93.184.216.34",dest_port="1935",direction="sent"} 52000
# HELP netrtmp_connection_bytes Current conntrack byte counter for an individual established connection ...
netrtmp_connection_bytes{dest_ip="93.184.216.34",dest_port="1935",direction="sent",local_port="38211"} 50000
netrtmp_connection_bytes{dest_ip="93.184.216.34",dest_port="1935",direction="received",local_port="38211"} 33000
```

### Useful PromQL examples

```promql
# Current number of outbound RTMP connections
sum(netrtmp_connections_active)

# Outbound bitrate (bytes/s) to the RTMP port
sum(rate(netrtmp_bytes_total{direction="sent"}[1m])) * 8

# Inbound bitrate (bytes/s)
sum(rate(netrtmp_bytes_total{direction="received"}[1m])) * 8

# Per-destination outbound bitrate
sum by (dest_ip) (rate(netrtmp_bytes_total{direction="sent"}[1m])) * 8

# Number of distinct RTMP destinations currently connected
count(netrtmp_connections_active)
```

---

## Requirements (on the monitored Ubuntu host)

- **iproute2** (`ss`) — pre-installed on Ubuntu.
- **conntrack-tools** (`conntrack`) — install it:
  ```bash
  sudo apt update && sudo apt install -y conntrack-tools
  sudo modprobe nf_conntrack   # load the kernel module
  ```
  To persist the module across reboots:
  ```bash
  echo "nf_conntrack" | sudo tee /etc/modules-load.d/nf_conntrack.conf
  ```
- The exporter must run as **root** (or a user with `CAP_NET_ADMIN` +
  `CAP_NET_RAW`) to read the conntrack table. The bundled systemd unit runs as
  root.

---

## Build

Requires Go 1.22+.

```bash
# Current host
make build             # -> bin/monitor-network-rtmp

# Cross-compile static Linux binaries for Ubuntu deployment
make build-linux       # -> bin/monitor-network-rtmp-linux-amd64
                       #    bin/monitor-network-rtmp-linux-arm64

make vet               # go vet ./...
```

The binaries are fully static (`CGO_ENABLED=0` for Linux) — just copy and run.

---

## Install on Ubuntu (systemd)

```bash
# On your build machine:
make build-linux

# Copy to the Ubuntu host:
scp bin/monitor-network-rtmp-linux-amd64  ubuntu-host:/usr/local/bin/monitor-network-rtmp
scp systemd/monitor-network-rtmp.service  ubuntu-host:/tmp/

# On the Ubuntu host:
sudo install -m 0755 /tmp/monitor-network-rtmp.service /etc/systemd/system/monitor-network-rtmp.service
sudo apt install -y conntrack-tools
sudo modprobe nf_conntrack
echo "nf_conntrack" | sudo tee /etc/modules-load.d/nf_conntrack.conf

sudo systemctl daemon-reload
sudo systemctl enable --now monitor-network-rtmp
sudo systemctl status monitor-network-rtmp

# Verify:
curl http://localhost:9101/metrics | grep netrtmp_
```

---

## Configuration

All options are CLI flags with matching environment variables:

| Flag | Env | Default | Description |
|------|-----|---------|-------------|
| `--target-port` | `RTMP_TARGET_PORT` | `1935` | TCP port to monitor for outbound connections |
| `--listen-address` | `RTMP_LISTEN_ADDRESS` | `:9101` | HTTP listen address |
| `--metrics-path` | `RTMP_METRICS_PATH` | `/metrics` | Path under which metrics are exposed |
| `--ss-path` | `RTMP_SS_PATH` | `ss` | Path to the `ss` binary |
| `--conntrack-path` | `RTMP_CONNTRACK_PATH` | `conntrack` | Path to the `conntrack` binary |
| `--scrape-timeout` | `RTMP_SCRAPE_TIMEOUT` | `5s` | Timeout for a single scrape (ss + conntrack) |

Examples:

```bash
# Monitor a different port
./monitor-network-rtmp --target-port 443

# Via env (useful for systemd / containers)
RTMP_TARGET_PORT=1935 RTMP_LISTEN_ADDRESS=:9101 ./monitor-network-rtmp
```

To change the port in systemd, edit the `Environment=` lines in the unit file
(or override without editing):

```bash
sudo systemctl edit monitor-network-rtmp
# In the editor:
[Service]
Environment=RTMP_TARGET_PORT=1935
Environment=RTMP_LISTEN_ADDRESS=:9101
```

---

## Prometheus integration

See [`prometheus/scrape.yml`](prometheus/scrape.yml) and
[`prometheus/alerts.yml`](prometheus/alerts.yml).

```yaml
scrape_configs:
  - job_name: "net-rtmp"
    scrape_interval: 15s
    metrics_path: /metrics
    static_configs:
      - targets: ["<ubuntu-host>:9101"]
```

Bundled alerts: exporter down, conntrack unavailable, no connections, no
outbound/inbound traffic, scrape errors.

---

## Endpoints

| Path | Description |
|------|-------------|
| `/metrics` | Prometheus metrics (configurable via `--metrics-path`) |
| `/` | Plain-text exporter info |
| `/healthz` | Health check (`ok`) |

---

## Run in Docker (optional)

The exporter needs **host networking** (to see the host's connections and
conntrack table) and the `NET_ADMIN` + `NET_RAW` capabilities (to read
conntrack). Linux only — `network_mode: host` is not supported on Docker Desktop
for macOS/Windows.

```bash
docker compose up -d --build
curl http://localhost:9101/metrics | grep netrtmp_
```

`docker-compose.yml` sets `RTMP_TARGET_PORT=1935` and `RTMP_LISTEN_ADDRESS=:9101`
by default — override via environment or the `command:` field. The host kernel
module `nf_conntrack` must still be loaded on the host (`modprobe nf_conntrack`).

---

## Notes & limitations

- The exporter is designed for **Linux** (Ubuntu). `ss`/`conntrack` are Linux
  tools; on macOS/dev it will still run but `netrtmp_up` will be `0`.
- `netrtmp_bytes_total` accumulates from exporter start; counters for a
  destination persist (cumulative) even after all its connections close.
- `netrtmp_connection_bytes` / `netrtmp_connection_packets` reflect the
  **current** conntrack counter of an open connection and disappear when the
  connection closes — use them for per-stream insight, not for long-term totals.
- conntrack must expose per-flow counters (the default on modern
  conntrack-tools). If `netrtmp_bytes_total` stays at 0 while
  `netrtmp_conntrack_up` is 1, verify your conntrack version shows
  `packets=`/`bytes=` in `conntrack -L` output.
