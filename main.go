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

	reg := prometheus.NewRegistry()
	reg.MustRegister(collector.New(cfg.SsPath, cfg.ConntrackPath, cfg.TargetPort, cfg.ScrapeTimeout))

	mux := http.NewServeMux()
	mux.Handle(cfg.MetricsPath, promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "monitor-network-rtmp (version %s)\nMetrics at %s\n", version, cfg.MetricsPath)
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})

	log.Printf("monitor-network-rtmp %s starting: listen=%s metrics=%s target_port=%d",
		version, cfg.ListenAddress, cfg.MetricsPath, cfg.TargetPort)

	srv := &http.Server{Addr: cfg.ListenAddress, Handler: mux}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("HTTP server failed: %v", err)
	}
}
