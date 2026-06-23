package config

import (
	"flag"
	"os"
	"strconv"
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
}

// Valid byte source values.
const (
	ByteSourceAuto      = "auto"
	ByteSourceConntrack = "conntrack"
	ByteSourceTCPInfo   = "ss-tcpinfo"
)

// Load parses CLI flags, allowing env overrides as defaults.
func Load() *Config {
	c := &Config{}

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

	flag.DurationVar(&c.ScrapeTimeout, "scrape-timeout",
		envDur("RTMP_SCRAPE_TIMEOUT", 5*time.Second),
		"Timeout for a single scrape (env: RTMP_SCRAPE_TIMEOUT)")

	flag.Parse()
	return c
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
