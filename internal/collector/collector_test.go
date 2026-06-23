package collector

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/vietanha34/monitor-network-rtmp/internal/config"
	"github.com/vietanha34/monitor-network-rtmp/internal/flow"
	"github.com/vietanha34/monitor-network-rtmp/internal/ss"
)

const (
	destA = "93.184.216.34"
	destB = "198.51.100.7"
)

// runScrape triggers one Collect and returns gathered metric families.
func runScrape(t *testing.T, c *Collector) map[string]*dto.MetricFamily {
	t.Helper()
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	out := map[string]*dto.MetricFamily{}
	for _, mf := range mfs {
		out[mf.GetName()] = mf
	}
	return out
}

func labelsMatch(lp []*dto.LabelPair, want map[string]string) bool {
	have := map[string]string{}
	for _, l := range lp {
		have[l.GetName()] = l.GetValue()
	}
	for k, v := range want {
		if have[k] != v {
			return false
		}
	}
	return true
}

func gaugeVal(mfs map[string]*dto.MetricFamily, name string, labels map[string]string) (float64, bool) {
	mf, ok := mfs[name]
	if !ok {
		return 0, false
	}
	for _, m := range mf.GetMetric() {
		if labelsMatch(m.GetLabel(), labels) {
			return m.GetGauge().GetValue(), true
		}
	}
	return 0, false
}

func counterVal(mfs map[string]*dto.MetricFamily, name string, labels map[string]string) (float64, bool) {
	mf, ok := mfs[name]
	if !ok {
		return 0, false
	}
	for _, m := range mf.GetMetric() {
		if labelsMatch(m.GetLabel(), labels) {
			return m.GetCounter().GetValue(), true
		}
	}
	return 0, false
}

func assertFloat(t *testing.T, got, want float64, msg string) {
	t.Helper()
	if got != want {
		t.Errorf("%s: got %v, want %v", msg, got, want)
	}
}

// newFakeCollector returns a Collector in explicit conntrack mode whose ss and
// byte-source functions are backed by the provided pointers, so tests can
// mutate them between scrapes.
func newFakeCollector(ssConns *[]ss.Connection, flows *[]flow.Flow, ssErrp, flowErrp *error) *Collector {
	c := New("ss", "conntrack", 1935, 5*time.Second, config.ByteSourceConntrack, nil)
	c.ssList = func(context.Context, string, int) ([]ss.Connection, error) { return *ssConns, *ssErrp }
	c.flowList = func(context.Context, string, int) ([]flow.Flow, error) { return *flows, *flowErrp }
	return c
}

// TestCollectorAccumulatorAndStaleRemoval verifies the core invariant:
// netrtmp_bytes_total is a monotonic counter that accumulates per-scrape
// deltas (surviving per-flow resets), per-connection gauges are removed when
// the connection closes, and connections_active updates.
func TestCollectorAccumulatorAndStaleRemoval(t *testing.T) {
	var (
		ssConns []ss.Connection
		flows []flow.Flow
		ssErr   error
		flowErr   error
	)
	c := newFakeCollector(&ssConns, &flows, &ssErr, &flowErr)

	// --- Scrape 1: 2 conns to destA, 1 to destB ---
	ssConns = []ss.Connection{
		{LocalIP: "10.0.0.5", LocalPort: 38211, DestIP: destA, DestPort: 1935},
		{LocalIP: "10.0.0.5", LocalPort: 38212, DestIP: destA, DestPort: 1935},
		{LocalIP: "10.0.0.5", LocalPort: 41983, DestIP: destB, DestPort: 1935},
	}
	flows = []flow.Flow{
		{LocalIP: "10.0.0.5", LocalPort: 38211, DestIP: destA, DestPort: 1935, SentBytes: 24000, SentPkts: 120, RecvBytes: 16000, RecvPkts: 80},
		{LocalIP: "10.0.0.5", LocalPort: 38212, DestIP: destA, DestPort: 1935, SentBytes: 2000, SentPkts: 10, RecvBytes: 1000, RecvPkts: 5},
		{LocalIP: "10.0.0.5", LocalPort: 41983, DestIP: destB, DestPort: 1935, SentBytes: 600, SentPkts: 3, RecvBytes: 400, RecvPkts: 2},
	}
	mfs := runScrape(t, c)

	assertFloat(t, mustGauge(t, mfs, "netrtmp_connections_active", lbl(destA)), 2, "active destA scrape1")
	assertFloat(t, mustGauge(t, mfs, "netrtmp_connections_active", lbl(destB)), 1, "active destB scrape1")
	assertFloat(t, mustCounter(t, mfs, "netrtmp_bytes_total", lblDir(destA, "sent")), 26000, "bytes destA sent scrape1")
	assertFloat(t, mustCounter(t, mfs, "netrtmp_bytes_total", lblDir(destA, "received")), 17000, "bytes destA recv scrape1")
	assertFloat(t, mustCounter(t, mfs, "netrtmp_bytes_total", lblDir(destB, "sent")), 600, "bytes destB sent scrape1")
	if v, ok := gaugeVal(mfs, "netrtmp_connection_bytes", lblConn(destA, 38212, "sent")); !ok || v != 2000 {
		t.Errorf("connection_bytes destA:38212 sent = %v (ok=%v), want 2000", v, ok)
	}

	// --- Scrape 2: drop conn 38212, bump 38211 (24000->50000 sent, 16000->33000 recv) ---
	ssConns = []ss.Connection{
		{LocalIP: "10.0.0.5", LocalPort: 38211, DestIP: destA, DestPort: 1935},
		{LocalIP: "10.0.0.5", LocalPort: 41983, DestIP: destB, DestPort: 1935},
	}
	flows = []flow.Flow{
		{LocalIP: "10.0.0.5", LocalPort: 38211, DestIP: destA, DestPort: 1935, SentBytes: 50000, SentPkts: 220, RecvBytes: 33000, RecvPkts: 160},
		{LocalIP: "10.0.0.5", LocalPort: 41983, DestIP: destB, DestPort: 1935, SentBytes: 600, SentPkts: 3, RecvBytes: 400, RecvPkts: 2},
	}
	mfs = runScrape(t, c)

	// Monotonic accumulator: 26000 + (50000-24000) = 52000.
	assertFloat(t, mustCounter(t, mfs, "netrtmp_bytes_total", lblDir(destA, "sent")), 52000, "bytes destA sent scrape2")
	assertFloat(t, mustCounter(t, mfs, "netrtmp_bytes_total", lblDir(destA, "received")), 34000, "bytes destA recv scrape2")
	assertFloat(t, mustCounter(t, mfs, "netrtmp_bytes_total", lblDir(destB, "sent")), 600, "bytes destB sent scrape2")
	assertFloat(t, mustGauge(t, mfs, "netrtmp_connections_active", lbl(destA)), 1, "active destA scrape2")
	if _, ok := gaugeVal(mfs, "netrtmp_connection_bytes", lblConn(destA, 38212, "sent")); ok {
		t.Error("connection_bytes destA:38212 sent should have been removed (stale)")
	}
	if v, ok := gaugeVal(mfs, "netrtmp_connection_bytes", lblConn(destA, 38211, "sent")); !ok || v != 50000 {
		t.Errorf("connection_bytes destA:38211 sent = %v (ok=%v), want 50000", v, ok)
	}
}

// TestCollectorFlowResetHandlesUnderflow verifies that when a per-flow counter
// resets (raw < last), the accumulator treats the current value as fresh
// instead of underflowing.
func TestCollectorFlowResetHandlesUnderflow(t *testing.T) {
	var (
		ssConns []ss.Connection
		flows []flow.Flow
		ssErr   error
		flowErr   error
	)
	c := newFakeCollector(&ssConns, &flows, &ssErr, &flowErr)

	ssConns = []ss.Connection{{LocalIP: "10.0.0.5", LocalPort: 38211, DestIP: destA, DestPort: 1935}}
	flows = []flow.Flow{{LocalIP: "10.0.0.5", LocalPort: 38211, DestIP: destA, DestPort: 1935, SentBytes: 100000, RecvBytes: 50000}}
	runScrape(t, c)

	flows = []flow.Flow{{LocalIP: "10.0.0.5", LocalPort: 38211, DestIP: destA, DestPort: 1935, SentBytes: 500, RecvBytes: 200}}
	mfs := runScrape(t, c)

	assertFloat(t, mustCounter(t, mfs, "netrtmp_bytes_total", lblDir(destA, "sent")), 100500, "bytes after flow reset (sent)")
	assertFloat(t, mustCounter(t, mfs, "netrtmp_bytes_total", lblDir(destA, "received")), 50200, "bytes after flow reset (recv)")
}

// TestCollectorEmptyTableKeepsByteSourceUp verifies that when the byte source
// returns no flows (and no error), byte_source_up stays 1 so the "byte source
// unavailable" alert does not fire on a host with no RTMP connections.
func TestCollectorEmptyTableKeepsByteSourceUp(t *testing.T) {
	var (
		ssConns []ss.Connection
		flows []flow.Flow
		ssErr   error
		flowErr   error
	)
	c := newFakeCollector(&ssConns, &flows, &ssErr, &flowErr)
	mfs := runScrape(t, c)

	if v, _ := gaugeVal(mfs, "netrtmp_up", nil); v != 1 {
		t.Errorf("netrtmp_up = %v, want 1 on empty table", v)
	}
	if v, _ := gaugeVal(mfs, "netrtmp_byte_source_up", nil); v != 1 {
		t.Errorf("netrtmp_byte_source_up = %v, want 1 on empty table (empty != unavailable)", v)
	}
	if mf, ok := mfs["netrtmp_connections_active"]; ok && len(mf.GetMetric()) != 0 {
		t.Errorf("expected no active-connection series, got %d", len(mf.GetMetric()))
	}
	if v, ok := counterVal(mfs, "netrtmp_scrape_errors_total", map[string]string{"source": "conntrack"}); !ok || v != 0 {
		t.Errorf("scrape_errors{conntrack} = %v (ok=%v), want 0", v, ok)
	}
}

func TestCollectorSSError(t *testing.T) {
	var (
		ssConns []ss.Connection
		flows []flow.Flow
		ssErr   error = errors.New("ss boom")
		flowErr   error
	)
	c := newFakeCollector(&ssConns, &flows, &ssErr, &flowErr)
	mfs := runScrape(t, c)

	if v, _ := gaugeVal(mfs, "netrtmp_up", nil); v != 0 {
		t.Errorf("netrtmp_up = %v, want 0 on ss error", v)
	}
	if v, _ := gaugeVal(mfs, "netrtmp_byte_source_up", nil); v != 1 {
		t.Errorf("netrtmp_byte_source_up = %v, want 1 (byte source still ok)", v)
	}
	if v, ok := counterVal(mfs, "netrtmp_scrape_errors_total", map[string]string{"source": "ss"}); !ok || v != 1 {
		t.Errorf("scrape_errors{ss} = %v (ok=%v), want 1", v, ok)
	}
}

func TestCollectorByteSourceError(t *testing.T) {
	var (
		ssConns []ss.Connection
		flows []flow.Flow
		ssErr   error
		flowErr   error = errors.New("conntrack boom")
	)
	c := newFakeCollector(&ssConns, &flows, &ssErr, &flowErr)
	mfs := runScrape(t, c)

	if v, _ := gaugeVal(mfs, "netrtmp_up", nil); v != 1 {
		t.Errorf("netrtmp_up = %v, want 1 (ss still ok)", v)
	}
	if v, _ := gaugeVal(mfs, "netrtmp_byte_source_up", nil); v != 0 {
		t.Errorf("netrtmp_byte_source_up = %v, want 0 on byte source error", v)
	}
	if v, ok := counterVal(mfs, "netrtmp_scrape_errors_total", map[string]string{"source": "conntrack"}); !ok || v != 1 {
		t.Errorf("scrape_errors{conntrack} = %v (ok=%v), want 1", v, ok)
	}
}

// TestCollectorScrapeErrorsPreinitialized verifies the scrape_errors_total
// series appear even on a healthy host with zero errors.
func TestCollectorScrapeErrorsPreinitialized(t *testing.T) {
	var (
		ssConns []ss.Connection
		flows []flow.Flow
		ssErr   error
		flowErr   error
	)
	c := newFakeCollector(&ssConns, &flows, &ssErr, &flowErr)
	mfs := runScrape(t, c)

	for _, src := range []string{"ss", "conntrack", "ss-tcpinfo"} {
		if v, ok := counterVal(mfs, "netrtmp_scrape_errors_total", map[string]string{"source": src}); !ok {
			t.Errorf("scrape_errors_total{source=%q} should exist even at 0", src)
		} else if v != 0 {
			t.Errorf("scrape_errors{%s} = %v, want 0", src, v)
		}
	}
}

// TestCollectorTCPInfoMode verifies that explicit ss-tcpinfo mode uses the
// injected byte source and labels errors with source="ss-tcpinfo".
func TestCollectorTCPInfoMode(t *testing.T) {
	var (
		ssConns []ss.Connection
		flows []flow.Flow
		ssErr   error
		flowErr   error
	)
	c := New("ss", "conntrack", 1935, 5*time.Second, config.ByteSourceTCPInfo, nil)
	c.ssList = func(context.Context, string, int) ([]ss.Connection, error) { return ssConns, ssErr }
	c.flowList = func(context.Context, string, int) ([]flow.Flow, error) { return flows, flowErr }

	ssConns = []ss.Connection{{LocalIP: "10.0.0.5", LocalPort: 44802, DestIP: "103.90.222.4", DestPort: 1935}}
	flows = []flow.Flow{{LocalIP: "10.0.0.5", LocalPort: 44802, DestIP: "103.90.222.4", DestPort: 1935, SentBytes: 1000, RecvBytes: 500, SentPkts: 10, RecvPkts: 5}}
	mfs := runScrape(t, c)

	if v, _ := gaugeVal(mfs, "netrtmp_byte_source_up", nil); v != 1 {
		t.Errorf("byte_source_up = %v, want 1", v)
	}
	assertFloat(t, mustCounter(t, mfs, "netrtmp_bytes_total", lblDir("103.90.222.4", "sent")), 1000, "tcpinfo sent")
	assertFloat(t, mustCounter(t, mfs, "netrtmp_bytes_total", lblDir("103.90.222.4", "received")), 500, "tcpinfo recv")

	// Now inject an error and confirm it is labeled ss-tcpinfo.
	flowErr = errors.New("ss -ti boom")
	mfs = runScrape(t, c)
	if v, _ := gaugeVal(mfs, "netrtmp_byte_source_up", nil); v != 0 {
		t.Errorf("byte_source_up = %v, want 0 on tcpinfo error", v)
	}
	if v, ok := counterVal(mfs, "netrtmp_scrape_errors_total", map[string]string{"source": "ss-tcpinfo"}); !ok || v != 1 {
		t.Errorf("scrape_errors{ss-tcpinfo} = %v (ok=%v), want 1", v, ok)
	}
}

// TestCollectorAutoFallsBackToConntrack verifies that auto mode, when the
// ss-tcpinfo probe fails, falls back to conntrack and uses the conntrack byte
// source for subsequent scrapes.
func TestCollectorAutoFallsBackToConntrack(t *testing.T) {
	var (
		ssConns []ss.Connection
		flows []flow.Flow
		ssErr   error
		flowErr   error
	)
	c := New("ss", "conntrack", 1935, 5*time.Second, config.ByteSourceAuto, nil)
	// Make the tcpinfo probe fail (simulates an old kernel / unsupported).
	c.tcpinfoList = func(context.Context, string, int) ([]flow.Flow, error) {
		return nil, errors.New("ss -ti does not expose TCP_INFO byte counters (kernel < 4.6?)")
	}
	c.ssList = func(context.Context, string, int) ([]ss.Connection, error) { return ssConns, ssErr }
	// flowList is conntrack.List by default for conntrack resolution; override
	// with a fake so the test does not depend on the real conntrack binary.
	c.flowList = func(context.Context, string, int) ([]flow.Flow, error) { return flows, flowErr }

	ssConns = []ss.Connection{{LocalIP: "10.0.0.5", LocalPort: 38211, DestIP: destA, DestPort: 1935}}
	flows = []flow.Flow{{LocalIP: "10.0.0.5", LocalPort: 38211, DestIP: destA, DestPort: 1935, SentBytes: 800, RecvBytes: 400}}
	mfs := runScrape(t, c)

	if c.ResolvedSource() != config.ByteSourceConntrack {
		t.Errorf("resolved source = %q, want conntrack", c.ResolvedSource())
	}
	assertFloat(t, mustCounter(t, mfs, "netrtmp_bytes_total", lblDir(destA, "sent")), 800, "auto-fallback sent")
	if v, _ := gaugeVal(mfs, "netrtmp_byte_source_up", nil); v != 1 {
		t.Errorf("byte_source_up = %v, want 1 (conntrack fallback ok)", v)
	}
}

// TestCollectorAutoUsesTCPInfo verifies that auto mode, when the ss-tcpinfo
// probe succeeds, locks in ss-tcpinfo and uses its results.
func TestCollectorAutoUsesTCPInfo(t *testing.T) {
	var ssErr error
	c := New("ss", "conntrack", 1935, 5*time.Second, config.ByteSourceAuto, nil)
	probeFlows := []flow.Flow{{LocalIP: "10.0.0.5", LocalPort: 44802, DestIP: "103.90.222.4", DestPort: 1935, SentBytes: 1234, RecvBytes: 567}}
	c.tcpinfoList = func(context.Context, string, int) ([]flow.Flow, error) { return probeFlows, nil }
	c.ssList = func(context.Context, string, int) ([]ss.Connection, error) {
		return []ss.Connection{{LocalIP: "10.0.0.5", LocalPort: 44802, DestIP: "103.90.222.4", DestPort: 1935}}, ssErr
	}

	mfs := runScrape(t, c)

	if c.ResolvedSource() != config.ByteSourceTCPInfo {
		t.Errorf("resolved source = %q, want ss-tcpinfo", c.ResolvedSource())
	}
	assertFloat(t, mustCounter(t, mfs, "netrtmp_bytes_total", lblDir("103.90.222.4", "sent")), 1234, "auto-tcpinfo sent")
	if v, _ := gaugeVal(mfs, "netrtmp_byte_source_up", nil); v != 1 {
		t.Errorf("byte_source_up = %v, want 1", v)
	}
}

// --- label helpers ---

func lbl(dest string) map[string]string {
	return map[string]string{"dest_ip": dest, "dest_port": "1935"}
}
func lblDir(dest, dir string) map[string]string {
	return map[string]string{"dest_ip": dest, "dest_port": "1935", "direction": dir}
}
func lblConn(dest string, localPort int, dir string) map[string]string {
	return map[string]string{"dest_ip": dest, "dest_port": "1935", "local_port": strconv.Itoa(localPort), "direction": dir}
}

func mustGauge(t *testing.T, mfs map[string]*dto.MetricFamily, name string, labels map[string]string) float64 {
	t.Helper()
	v, ok := gaugeVal(mfs, name, labels)
	if !ok {
		t.Fatalf("gauge %s %v not found", name, labels)
	}
	return v
}
func mustCounter(t *testing.T, mfs map[string]*dto.MetricFamily, name string, labels map[string]string) float64 {
	t.Helper()
	v, ok := counterVal(mfs, name, labels)
	if !ok {
		t.Fatalf("counter %s %v not found", name, labels)
	}
	return v
}

// TestCollectorConstLabels verifies that constant labels (hostname, env, ...)
// passed to New are attached to every exported metric series.
func TestCollectorConstLabels(t *testing.T) {
	constLabels := prometheus.Labels{"hostname": "host-1", "env": "prod"}
	c := New("ss", "conntrack", 1935, 5*time.Second, config.ByteSourceConntrack, constLabels)
	c.ssList = func(context.Context, string, int) ([]ss.Connection, error) {
		return []ss.Connection{{LocalIP: "10.0.0.5", LocalPort: 38211, DestIP: destA, DestPort: 1935}}, nil
	}
	f := []flow.Flow{{LocalIP: "10.0.0.5", LocalPort: 38211, DestIP: destA, DestPort: 1935, SentBytes: 100, RecvBytes: 50}}
	c.flowList = func(context.Context, string, int) ([]flow.Flow, error) { return f, nil }

	mfs := runScrape(t, c)

	// Every series across all metric families must carry the const labels.
	for name, mf := range mfs {
		if len(mf.GetMetric()) == 0 {
			continue
		}
		for i, m := range mf.GetMetric() {
			have := map[string]string{}
			for _, lp := range m.GetLabel() {
				have[lp.GetName()] = lp.GetValue()
			}
			if have["hostname"] != "host-1" {
				t.Errorf("%s[%d]: missing/wrong hostname label: %v", name, i, have)
			}
			if have["env"] != "prod" {
				t.Errorf("%s[%d]: missing/wrong env label: %v", name, i, have)
			}
		}
	}

	// Sanity: a per-connection series still has the variable labels too.
	want := map[string]string{
		"hostname":   "host-1",
		"env":        "prod",
		"dest_ip":    destA,
		"dest_port":  "1935",
		"local_port": "38211",
		"direction":  "sent",
	}
	if _, ok := gaugeVal(mfs, "netrtmp_connection_bytes", want); !ok {
		t.Errorf("connection_bytes with const+variable labels not found (want %v)", want)
	}
}
