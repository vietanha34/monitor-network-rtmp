package config

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all exporter runtime configuration.
type Config struct {
	TargetPort    int
	ListenAddress string
	MetricsPath   string
	SsPath        string
	ConntrackPath string
	ByteSource    string
	ScrapeTimeout time.Duration
	// Labels are constant labels applied to every exported metric series.
	// Always includes "hostname" (auto-detected, overridable) plus any custom
	// labels from --label / RTMP_LABELS.
	Labels map[string]string
}

// Valid byte source values.
const (
	ByteSourceAuto      = "auto"
	ByteSourceConntrack = "conntrack"
	ByteSourceTCPInfo   = "ss-tcpinfo"
)

// labelFlag collects repeated --label key=value flags into a map.
type labelFlag map[string]string

func (l labelFlag) String() string {
	if len(l) == 0 {
		return ""
	}
	parts := make([]string, 0, len(l))
	for k, v := range l {
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, ",")
}

func (l labelFlag) Set(s string) error {
	k, v, ok := strings.Cut(s, "=")
	if !ok || k == "" {
		return fmt.Errorf("label %q must be key=value", s)
	}
	if invalidLabelKey(k) {
		return fmt.Errorf("label key %q is not a valid Prometheus label name", k)
	}
	l[k] = v
	return nil
}

// Load parses CLI flags, allowing env overrides as defaults.
func Load() *Config {
	c := &Config{}
	cliLabels := labelFlag{}

	flag.IntVar(&c.TargetPort, "target-port",
		envInt("RTMP_TARGET_PORT", 1935),
		"TCP port to monitor for outbound connections (env: RTMP_TARGET_PORT)")

	flag.StringVar(&c.ListenAddress, "listen-address",
		envStr("RTMP_LISTEN_ADDRESS", ":9101"),
		"Address to listen on for HTTP (env: RTMP_LISTEN_ADDRESS)")

	flag.StringVar(&c.MetricsPath, "metrics-path",
		envStr("RTMP_METRICS_PATH", "/metrics"),
		"Path under which to expose Prometheus metrics (env: RTMP_METRICS_PATH)")

	flag.StringVar(&c.SsPath, "ss-path",
		envStr("RTMP_SS_PATH", "ss"),
		"Path to the ss binary (env: RTMP_SS_PATH)")

	flag.StringVar(&c.ConntrackPath, "conntrack-path",
		envStr("RTMP_CONNTRACK_PATH", "conntrack"),
		"Path to the conntrack binary, used when --byte-source=conntrack (env: RTMP_CONNTRACK_PATH)")

	flag.StringVar(&c.ByteSource, "byte-source",
		envStr("RTMP_BYTE_SOURCE", ByteSourceAuto),
		"Source for per-connection byte counters: auto|conntrack|ss-tcpinfo. "+
			"\"ss-tcpinfo\" uses ss TCP_INFO (no conntrack-tools needed, kernel>=4.6). "+
			"\"auto\" tries ss-tcpinfo and falls back to conntrack (env: RTMP_BYTE_SOURCE)")

	flag.Var(cliLabels, "label",
		"Custom Prometheus label to attach to every metric (repeatable, e.g. --label env=prod --label region=ap). "+
			"Also set via RTMP_LABELS=key1=val1,key2=val2. The default \"hostname\" label is auto-added unless overridden.")

	flag.DurationVar(&c.ScrapeTimeout, "scrape-timeout",
		envDur("RTMP_SCRAPE_TIMEOUT", 5*time.Second),
		"Timeout for a single scrape (env: RTMP_SCRAPE_TIMEOUT)")

	flag.Parse()

	c.Labels = buildLabels(cliLabels)
	return c
}

// buildLabels merges RTMP_LABELS env + --label flags and adds the default
// "hostname" label (auto-detected) unless the user overrode it.
func buildLabels(cliLabels map[string]string) map[string]string {
	labels := map[string]string{}

	// Env labels first (lower precedence than explicit --label).
	if envv, ok := os.LookupEnv("RTMP_LABELS"); ok && envv != "" {
		for _, pair := range strings.Split(envv, ",") {
			k, v, ok := strings.Cut(strings.TrimSpace(pair), "=")
			if !ok || k == "" || invalidLabelKey(k) {
				continue
			}
			labels[k] = strings.TrimSpace(v)
		}
	}
	// CLI labels override env labels.
	for k, v := range cliLabels {
		labels[k] = v
	}
	// Default hostname label if not set by the user.
	if _, ok := labels["hostname"]; !ok {
		if hn, err := os.Hostname(); err == nil && hn != "" {
			labels["hostname"] = hn
		}
	}
	return labels
}

// invalidLabelKey reports whether k is not a valid Prometheus label name.
// Prometheus label names match [a-zA-Z_][a-zA-Z0-9_]*.
func invalidLabelKey(k string) bool {
	if k == "" {
		return true
	}
	for i, r := range k {
		if r == '_' || ('a' <= r && r <= 'z') || ('A' <= r && r <= 'Z') {
			continue
		}
		if i > 0 && '0' <= r && r <= '9' {
			continue
		}
		return true
	}
	return false
}

// ValidByteSource reports whether the byte source value is recognized.
func ValidByteSource(s string) bool {
	return s == ByteSourceAuto || s == ByteSourceConntrack || s == ByteSourceTCPInfo
}

func envStr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envDur(key string, def time.Duration) time.Duration {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
