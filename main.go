// Command monitor-network-rtmp is a Prometheus exporter that monitors
// outbound TCP connections (default port 1935/RTMP) and the bytes/packets
// transferred over them, using ss + conntrack.
package main

import (
	"fmt"
	"log"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/vietanha34/monitor-network-rtmp/internal/collector"
	"github.com/vietanha34/monitor-network-rtmp/internal/config"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	cfg := config.Load()

	if !config.ValidByteSource(cfg.ByteSource) {
		log.Fatalf("invalid --byte-source %q: must be auto, conntrack, or ss-tcpinfo", cfg.ByteSource)
	}

	reg := prometheus.NewRegistry()
	reg.MustRegister(collector.New(cfg.SsPath, cfg.ConntrackPath, cfg.TargetPort, cfg.ScrapeTimeout, cfg.ByteSource, prometheus.Labels(cfg.Labels)))

	mux := http.NewServeMux()
	mux.Handle(cfg.MetricsPath, promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "monitor-network-rtmp (version %s)\nMetrics at %s\n", version, cfg.MetricsPath)
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})

	log.Printf("monitor-network-rtmp %s starting: listen=%s metrics=%s target_port=%d byte_source=%s labels=%v",
		version, cfg.ListenAddress, cfg.MetricsPath, cfg.TargetPort, cfg.ByteSource, cfg.Labels)

	srv := &http.Server{Addr: cfg.ListenAddress, Handler: mux}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("HTTP server failed: %v", err)
	}
}
