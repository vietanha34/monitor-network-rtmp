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
| byte source (configurable) | `conntrack -L ...` **or** `ss -H -t -n -i state established` | Per-connection byte/packet counters for both directions |

It then matches byte-source flows to `ss` connections by 4-tuple
`(local_ip, local_port, dest_ip, dest_port)` and exposes:

- **Aggregate** connection count and a **monotonic** total-bytes counter per
  `dest_ip`.
- **Per-connection** current byte/packet counters (gauge) so you can see each
  individual RTMP stream.

### Byte source: `conntrack` vs `ss-tcpinfo`

The exporter can get per-connection byte counters from one of two sources,
selected via `--byte-source`:

| Mode | Command | Requires | Kernel |
|------|---------|----------|--------|
| `ss-tcpinfo` | `ss -ti` (TCP_INFO) | only `ss` (already used) | ≥ 4.6 |
| `conntrack` | `conntrack -L` | `conntrack-tools` + `nf_conntrack` module | any |
| `auto` (default) | tries `ss-tcpinfo`, falls back to `conntrack` on error | — | — |

**Prefer `ss-tcpinfo`** when possible: it needs **no extra packages** and **no
kernel module** — the same `ss` binary already used for connection listing
also exposes `bytes_sent`/`bytes_received` per socket via `struct tcp_info`
(kernel ≥ 4.6, i.e. Ubuntu 18.04+). Use `conntrack` only on older kernels or
when you specifically want conntrack-table semantics. `auto` picks the best
available source on startup and locks it in.

### Why an accumulator?

Per-connection byte counters (from either source) **reset to 0 whenever the
connection closes**. A raw sum is therefore not a monotonic Prometheus
counter. The exporter keeps an internal per-flow accumulator and emits only
the **delta** to `netrtmp_bytes_total`, so that metric is a proper
ever-increasing counter safe for `rate()` — even across connection churn and
counter resets.

---

## Metrics

Every metric series carries the **constant labels** from `--label` / `RTMP_LABELS`
(see [Multi-server deployment](#multi-server-deployment-labels)), including the
auto-added `hostname` label. The variable labels listed below are in addition to
those.

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `netrtmp_up` | gauge | — | `1` if the `ss` connection scrape succeeded, else `0` |
| `netrtmp_byte_source_up` | gauge | — | `1` if the byte-source scrape (conntrack or ss-tcpinfo) succeeded, else `0` |
| `netrtmp_connections_active` | gauge | `dest_ip`, `dest_port` | Number of established outbound connections to the target port |
| `netrtmp_bytes_total` | counter | `dest_ip`, `dest_port`, `direction` | Total bytes transferred (monotonic; `direction` = `sent` or `received`) |
| `netrtmp_connection_bytes` | gauge | `dest_ip`, `dest_port`, `local_port`, `direction` | Current byte counter for a single open connection |
| `netrtmp_connection_packets` | gauge | `dest_ip`, `dest_port`, `local_port`, `direction` | Current packet counter for a single open connection |
| `netrtmp_scrape_duration_seconds` | gauge | — | Duration of the last scrape |
| `netrtmp_scrape_errors_total` | counter | `source` | Total scrape errors (`source` = `ss`, `conntrack`, or `ss-tcpinfo`) |

Stale per-connection series are automatically removed when the connection
closes (cardinality stays bounded to currently-open connections).

### Example output

```
# HELP netrtmp_connections_active Number of established outbound TCP connections to the target port.
netrtmp_connections_active{dest_ip="93.184.216.34",dest_port="1935",env="prod",hostname="rtmp-src-1"} 2
# HELP netrtmp_bytes_total Total bytes transferred over established outbound connections ...
netrtmp_bytes_total{dest_ip="93.184.216.34",dest_port="1935",direction="received",env="prod",hostname="rtmp-src-1"} 34000
netrtmp_bytes_total{dest_ip="93.184.216.34",dest_port="1935",direction="sent",env="prod",hostname="rtmp-src-1"} 52000
# HELP netrtmp_connection_bytes Current byte counter for an individual established connection ...
netrtmp_connection_bytes{dest_ip="93.184.216.34",dest_port="1935",direction="sent",env="prod",hostname="rtmp-src-1",local_port="38211"} 50000
netrtmp_connection_bytes{dest_ip="93.184.216.34",dest_port="1935",direction="received",env="prod",hostname="rtmp-src-1",local_port="38211"} 33000
```

### Useful PromQL examples

```promql
# Total outbound RTMP connections across all hosts
sum(netrtmp_connections_active)

# Outbound bitrate per host (bytes/s)
sum by (hostname) (rate(netrtmp_bytes_total{direction="sent"}[1m])) * 8

# Inbound bitrate per host
sum by (hostname) (rate(netrtmp_bytes_total{direction="received"}[1m])) * 8

# Per-destination outbound bitrate, broken down per host
sum by (hostname, dest_ip) (rate(netrtmp_bytes_total{direction="sent"}[1m])) * 8

# Total bitrate across all hosts and destinations
sum(rate(netrtmp_bytes_total{direction="sent"}[1m])) * 8

# Number of distinct RTMP destinations currently connected (per host)
count by (hostname) (netrtmp_connections_active)
```

---

## Requirements (on the monitored Ubuntu host)

- **iproute2** (`ss`) — pre-installed on Ubuntu. Used for connection listing
  and, in `ss-tcpinfo` mode, for byte counters too.
- **conntrack-tools** (`conntrack`) — **only required** when
  `--byte-source=conntrack` (or `auto` falls back to it). Not needed for
  `--byte-source=ss-tcpinfo`. To install:
  ```bash
  sudo apt update && sudo apt install -y conntrack-tools
  sudo modprobe nf_conntrack   # load the kernel module
  ```
  To persist the module across reboots:
  ```bash
  echo "nf_conntrack" | sudo tee /etc/modules-load.d/nf_conntrack.conf
  ```
- The exporter must run as **root** (or a user with `CAP_NET_ADMIN` +
  `CAP_NET_RAW`) to read all sockets / the conntrack table. The bundled
  systemd unit runs as root.

> **No conntrack available?** Run with `--byte-source=ss-tcpinfo` (or
> `auto`, the default). On kernel ≥ 4.6 this needs only `ss` — no extra
> packages, no kernel module.

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
# conntrack-tools only needed for --byte-source=conntrack; ss-tcpinfo/auto need only `ss`.
sudo apt install -y conntrack-tools
sudo modprobe nf_conntrack
echo "nf_conntrack" | sudo tee /etc/modules-load.d/nf_conntrack.conf

sudo systemctl daemon-reload
sudo systemctl enable --now monitor-network-rtmp
sudo systemctl status monitor-network-rtmp

# Verify:
curl http://localhost:9101/metrics | grep netrtmp_
```

> **No conntrack?** Skip the `apt install conntrack-tools` / `modprobe`
> steps and set `RTMP_BYTE_SOURCE=ss-tcpinfo` in the systemd unit (or leave
> the default `auto`, which falls back to `ss-tcpinfo`).

---

## Upgrade guide

### v0.2.0 -> v0.2.1 (add constant labels)

v0.2.1 is a **drop-in replacement** — same metrics, same CLI flags, no breaking
changes. The only new feature is constant labels (`hostname` auto-added + custom
`--label` / `RTMP_LABELS`). Steps:

```bash
# 1. Download the new binary (on each Ubuntu host, or copy from build machine)
wget https://github.com/vietanha34/monitor-network-rtmp/releases/download/v0.2.1/monitor-network-rtmp-linux-amd64
wget https://github.com/vietanha34/monitor-network-rtmp/releases/download/v0.2.1/checksums.txt
sha256sum -c checksums.txt --ignore-missing

# 2. Stop the service
sudo systemctl stop monitor-network-rtmp

# 3. Replace the binary
sudo install -m 0755 monitor-network-rtmp-linux-amd64 /usr/local/bin/monitor-network-rtmp

# 4. (Optional) add custom labels via systemd override — does NOT edit the unit file
sudo systemctl edit monitor-network-rtmp
#   In the editor, add:
#   [Service]
#   Environment=RTMP_LABELS=env=prod,region=ap-southeast-1

# 5. Restart and verify
sudo systemctl start monitor-network-rtmp
sudo systemctl status monitor-network-rtmp

# 6. Confirm the new version + labels appear
curl -s http://localhost:9101/metrics | grep -E 'netrtmp_up\{'
# Expect: netrtmp_up{hostname="<this-host>",...} 1
```

> **No labels needed?** Skip step 4 — the `hostname` label is auto-added even
> with zero config, so existing dashboards keep working and you can already
> tell hosts apart by `hostname`.

> **Dashboards/alerts:** no changes required. v0.2.1 adds labels to existing
> series (same metric names), so PromQL like
> `sum(rate(netrtmp_bytes_total{direction="sent"}[1m]))` still works — you can
> now also break it down with `sum by (hostname) (...)`.

### v0.1.0 -> v0.2.0 (ss-tcpinfo + metric rename)

If you're on v0.1.0, note the **breaking change** in v0.2.0:
`netrtmp_conntrack_up` was renamed to **`netrtmp_byte_source_up`**. Update any
dashboards/alerts that reference the old name. Then follow the same
download/replace/restart steps above with the v0.2.1 binary (v0.2.1 includes
all v0.2.0 changes).

---

## Configuration

All options are CLI flags with matching environment variables:

| Flag | Env | Default | Description |
|------|-----|---------|-------------|
| `--target-port` | `RTMP_TARGET_PORT` | `1935` | TCP port to monitor for outbound connections |
| `--listen-address` | `RTMP_LISTEN_ADDRESS` | `:9101` | HTTP listen address |
| `--metrics-path` | `RTMP_METRICS_PATH` | `/metrics` | Path under which metrics are exposed |
| `--byte-source` | `RTMP_BYTE_SOURCE` | `auto` | Byte-counter source: `auto` \| `conntrack` \| `ss-tcpinfo` |
| `--label` (repeatable) | `RTMP_LABELS` | `hostname=<auto>` | Custom constant labels on every metric (e.g. `--label env=prod`). `RTMP_LABELS=key1=val1,key2=val2`. The `hostname` label is auto-added from `os.Hostname()` unless you override it. |
| `--ss-path` | `RTMP_SS_PATH` | `ss` | Path to the `ss` binary |
| `--conntrack-path` | `RTMP_CONNTRACK_PATH` | `conntrack` | Path to the `conntrack` binary (only used when byte-source=conntrack) |
| `--scrape-timeout` | `RTMP_SCRAPE_TIMEOUT` | `5s` | Timeout for a single scrape |

Examples:

```bash
# Monitor a different port
./monitor-network-rtmp --target-port 443

# No conntrack installed — use ss TCP_INFO (kernel >= 4.6)
./monitor-network-rtmp --byte-source ss-tcpinfo

# Add custom labels (hostname is auto-added; env/region are custom)
./monitor-network-rtmp --label env=prod --label region=ap-southeast-1

# Override the auto-detected hostname
./monitor-network-rtmp --label hostname=rtmp-src-01

# Via env (useful for systemd / containers)
RTMP_TARGET_PORT=1935 RTMP_LABELS=env=prod,region=ap ./monitor-network-rtmp
```

To change the port in systemd, edit the `Environment=` lines in the unit file
(or override without editing):

```bash
sudo systemctl edit monitor-network-rtmp
# In the editor:
[Service]
Environment=RTMP_TARGET_PORT=1935
Environment=RTMP_LISTEN_ADDRESS=:9101
Environment=RTMP_LABELS=env=prod,region=ap-southeast-1
```

---

## Multi-server deployment (labels)

When running the exporter on many hosts, attach constant labels so Prometheus
can tell series apart. The exporter **auto-adds a `hostname` label** (from
`os.Hostname()`) to every metric, so even with zero configuration each host's
series are distinguishable. Add more labels with `--label` (repeatable) or
`RTMP_LABELS` (comma-separated):

```bash
# Per-host systemd override (sudo systemctl edit monitor-network-rtmp)
[Service]
Environment=RTMP_LABELS=env=prod,region=ap-southeast-1,dc=hcm1
```

```bash
# CLI
./monitor-network-rtmp --label env=prod --label region=ap-southeast-1 --label dc=hcm1
```

Every exported series then carries the labels, e.g.:

```
netrtmp_bytes_total{dest_ip="103.90.222.4",dest_port="1935",direction="sent",env="prod",hostname="rtmp-src-1",region="ap-southeast-1",dc="hcm1"} 1.8048910687e+10
```

Then aggregate across hosts in PromQL:

```promql
# Outbound bitrate per host
sum by (hostname) (rate(netrtmp_bytes_total{direction="sent"}[1m])) * 8

# Outbound bitrate per region (across all hosts in that region)
sum by (region) (rate(netrtmp_bytes_total{direction="sent"}[1m])) * 8
```

> **Tip:** You can also use Prometheus `static_configs` `labels:` to add labels
> at scrape time, but exporter-side `--label` is preferable when the label
> value is a property of the host itself (so it's consistent regardless of which
> Prometheus scrapes it, and shows up in `curl localhost:9101/metrics`).

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

Bundled alerts: exporter down, byte source unavailable, no connections, no
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
conntrack table). For `--byte-source=conntrack`, also grant the `NET_ADMIN` +
`NET_RAW` capabilities (to read conntrack); for `--byte-source=ss-tcpinfo` or
`auto`, `NET_RAW` alone suffices to read socket tables. Linux only —
`network_mode: host` is not supported on Docker Desktop for macOS/Windows.

```bash
docker compose up -d --build
curl http://localhost:9101/metrics | grep netrtmp_
```

`docker-compose.yml` sets `RTMP_TARGET_PORT=1935`, `RTMP_LISTEN_ADDRESS=:9101`,
and `RTMP_BYTE_SOURCE=auto` by default — override via environment or the
`command:` field. For conntrack mode, the host kernel module `nf_conntrack`
must be loaded on the host (`modprobe nf_conntrack`); for ss-tcpinfo mode it is
not needed.

---

## Notes & limitations

- The exporter is designed for **Linux** (Ubuntu). `ss`/`conntrack` are Linux
  tools; on macOS/dev it will still run but `netrtmp_up` will be `0`.
- `netrtmp_bytes_total` accumulates from exporter start; counters for a
  destination persist (cumulative) even after all its connections close.
- `netrtmp_connection_bytes` / `netrtmp_connection_packets` reflect the
  **current** byte/packet counter of an open connection (from whichever byte
  source is active) and disappear when the connection closes — use them for
  per-stream insight, not for long-term totals.
- **ss-tcpinfo mode** requires kernel ≥ 4.6 (`bytes_sent`/`bytes_received` in
  `struct tcp_info`). If `netrtmp_bytes_total` stays at 0 while
  `netrtmp_byte_source_up` is 1, check that `ss -ti` prints `bytes_sent:`
  for your connections (older kernels lack the field — use
  `--byte-source=conntrack` instead).
- **conntrack mode** requires conntrack-tools to expose per-flow counters (the
  default on modern versions). Verify with `conntrack -L -p tcp --dport 1935`
  that `packets=`/`bytes=` fields appear.
