package collector

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/vietanha34/monitor-network-rtmp/internal/conntrack"
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

// newFakeCollector returns a Collector whose ss/conntrack sources are backed
// by the provided pointers, so tests can mutate them between scrapes.
func newFakeCollector(ssConns *[]ss.Connection, ctFlows *[]conntrack.Flow, ssErrp, ctErrp *error) *Collector {
	c := New("ss", "conntrack", 1935, 5*time.Second)
	c.ssList = func(context.Context, string, int) ([]ss.Connection, error) { return *ssConns, *ssErrp }
	c.ctList = func(context.Context, string, int) ([]conntrack.Flow, error) { return *ctFlows, *ctErrp }
	return c
}

// TestCollectorAccumulatorAndStaleRemoval verifies the core invariant:
// netrtmp_bytes_total is a monotonic counter that accumulates per-scrape
// deltas (surviving per-flow conntrack resets), per-connection gauges are
// removed when the connection closes, and connections_active updates.
func TestCollectorAccumulatorAndStaleRemoval(t *testing.T) {
	var (
		ssConns []ss.Connection
		ctFlows []conntrack.Flow
		ssErr   error
		ctErr   error
	)
	c := newFakeCollector(&ssConns, &ctFlows, &ssErr, &ctErr)

	// --- Scrape 1: 2 conns to destA, 1 to destB ---
	ssConns = []ss.Connection{
		{LocalIP: "10.0.0.5", LocalPort: 38211, DestIP: destA, DestPort: 1935},
		{LocalIP: "10.0.0.5", LocalPort: 38212, DestIP: destA, DestPort: 1935},
		{LocalIP: "10.0.0.5", LocalPort: 41983, DestIP: destB, DestPort: 1935},
	}
	ctFlows = []conntrack.Flow{
		{LocalIP: "10.0.0.5", LocalPort: 38211, DestIP: destA, DestPort: 1935, SentBytes: 24000, SentPkts: 120, RecvBytes: 16000, RecvPkts: 80},
		{LocalIP: "10.0.0.5", LocalPort: 38212, DestIP: destA, DestPort: 1935, SentBytes: 2000, SentPkts: 10, RecvBytes: 1000, RecvPkts: 5},
		{LocalIP: "10.0.0.5", LocalPort: 41983, DestIP: destB, DestPort: 1935, SentBytes: 600, SentPkts: 3, RecvBytes: 400, RecvPkts: 2},
	}
	mfs := runScrape(t, c)

	assertFloat(t, mustGauge(t, mfs, "netrtmp_connections_active", lbl(destA)), 2, "active destA scrape1")
	assertFloat(t, mustGauge(t, mfs, "netrtmp_connections_active", lbl(destB)), 1, "active destB scrape1")
	// Aggregate bytes = sum of per-flow raw counters on first scrape.
	assertFloat(t, mustCounter(t, mfs, "netrtmp_bytes_total", lblDir(destA, "sent")), 26000, "bytes destA sent scrape1")
	assertFloat(t, mustCounter(t, mfs, "netrtmp_bytes_total", lblDir(destA, "received")), 17000, "bytes destA recv scrape1")
	assertFloat(t, mustCounter(t, mfs, "netrtmp_bytes_total", lblDir(destB, "sent")), 600, "bytes destB sent scrape1")
	// Per-connection gauge present.
	if v, ok := gaugeVal(mfs, "netrtmp_connection_bytes", lblConn(destA, 38212, "sent")); !ok || v != 2000 {
		t.Errorf("connection_bytes destA:38212 sent = %v (ok=%v), want 2000", v, ok)
	}

	// --- Scrape 2: drop conn 38212, bump 38211 (24000->50000 sent, 16000->33000 recv) ---
	ssConns = []ss.Connection{
		{LocalIP: "10.0.0.5", LocalPort: 38211, DestIP: destA, DestPort: 1935},
		{LocalIP: "10.0.0.5", LocalPort: 41983, DestIP: destB, DestPort: 1935},
	}
	ctFlows = []conntrack.Flow{
		{LocalIP: "10.0.0.5", LocalPort: 38211, DestIP: destA, DestPort: 1935, SentBytes: 50000, SentPkts: 220, RecvBytes: 33000, RecvPkts: 160},
		{LocalIP: "10.0.0.5", LocalPort: 41983, DestIP: destB, DestPort: 1935, SentBytes: 600, SentPkts: 3, RecvBytes: 400, RecvPkts: 2},
	}
	mfs = runScrape(t, c)

	// Monotonic accumulator: 26000 + (50000-24000) = 52000.
	assertFloat(t, mustCounter(t, mfs, "netrtmp_bytes_total", lblDir(destA, "sent")), 52000, "bytes destA sent scrape2")
	assertFloat(t, mustCounter(t, mfs, "netrtmp_bytes_total", lblDir(destA, "received")), 34000, "bytes destA recv scrape2")
	// destB unchanged -> delta 0 -> counter unchanged.
	assertFloat(t, mustCounter(t, mfs, "netrtmp_bytes_total", lblDir(destB, "sent")), 600, "bytes destB sent scrape2")
	// Active count dropped to 1 for destA.
	assertFloat(t, mustGauge(t, mfs, "netrtmp_connections_active", lbl(destA)), 1, "active destA scrape2")
	// Stale per-conn gauge for closed connection 38212 must be removed.
	if _, ok := gaugeVal(mfs, "netrtmp_connection_bytes", lblConn(destA, 38212, "sent")); ok {
		t.Error("connection_bytes destA:38212 sent should have been removed (stale)")
	}
	// Remaining connection reflects bumped raw counter.
	if v, ok := gaugeVal(mfs, "netrtmp_connection_bytes", lblConn(destA, 38211, "sent")); !ok || v != 50000 {
		t.Errorf("connection_bytes destA:38211 sent = %v (ok=%v), want 50000", v, ok)
	}
}

// TestCollectorFlowResetHandlesUnderflow verifies that when a conntrack flow
// counter resets (raw < last), the accumulator treats the current value as
// fresh instead of underflowing.
func TestCollectorFlowResetHandlesUnderflow(t *testing.T) {
	var (
		ssConns []ss.Connection
		ctFlows []conntrack.Flow
		ssErr   error
		ctErr   error
	)
	c := newFakeCollector(&ssConns, &ctFlows, &ssErr, &ctErr)

	ssConns = []ss.Connection{{LocalIP: "10.0.0.5", LocalPort: 38211, DestIP: destA, DestPort: 1935}}
	ctFlows = []conntrack.Flow{{LocalIP: "10.0.0.5", LocalPort: 38211, DestIP: destA, DestPort: 1935, SentBytes: 100000, RecvBytes: 50000}}
	runScrape(t, c)

	// Same connection reappears with a freshly-reset (smaller) counter —
	// simulates conntrack flow reuse/reset. Delta must be the new raw value,
	// not a huge underflow.
	ctFlows = []conntrack.Flow{{LocalIP: "10.0.0.5", LocalPort: 38211, DestIP: destA, DestPort: 1935, SentBytes: 500, RecvBytes: 200}}
	mfs := runScrape(t, c)

	// 100000 (first) + 500 (reset treated as fresh) = 100500
	assertFloat(t, mustCounter(t, mfs, "netrtmp_bytes_total", lblDir(destA, "sent")), 100500, "bytes after flow reset (sent)")
	assertFloat(t, mustCounter(t, mfs, "netrtmp_bytes_total", lblDir(destA, "received")), 50200, "bytes after flow reset (recv)")
}

// TestCollectorEmptyTableKeepsConntrackUp verifies the fix for review issue #2:
// when conntrack returns no flows (and no error), conntrack_up must stay 1
// so the "conntrack unavailable" alert does not fire on a host with no RTMP
// connections.
func TestCollectorEmptyTableKeepsConntrackUp(t *testing.T) {
	var (
		ssConns []ss.Connection
		ctFlows []conntrack.Flow
		ssErr   error
		ctErr   error
	)
	c := newFakeCollector(&ssConns, &ctFlows, &ssErr, &ctErr)
	ssConns = nil
	ctFlows = nil
	mfs := runScrape(t, c)

	if v, _ := gaugeVal(mfs, "netrtmp_up", nil); v != 1 {
		t.Errorf("netrtmp_up = %v, want 1 on empty table", v)
	}
	if v, _ := gaugeVal(mfs, "netrtmp_conntrack_up", nil); v != 1 {
		t.Errorf("netrtmp_conntrack_up = %v, want 1 on empty table (empty != unavailable)", v)
	}
	if mf, ok := mfs["netrtmp_connections_active"]; ok && len(mf.GetMetric()) != 0 {
		t.Errorf("expected no active-connection series, got %d", len(mf.GetMetric()))
	}
	// scrape_errors_total{source="conntrack"} must remain 0.
	if v, ok := counterVal(mfs, "netrtmp_scrape_errors_total", map[string]string{"source": "conntrack"}); !ok || v != 0 {
		t.Errorf("scrape_errors{conntrack} = %v (ok=%v), want 0", v, ok)
	}
}

func TestCollectorSSError(t *testing.T) {
	var (
		ssConns []ss.Connection
		ctFlows []conntrack.Flow
		ssErr   error = errors.New("ss boom")
		ctErr   error
	)
	c := newFakeCollector(&ssConns, &ctFlows, &ssErr, &ctErr)
	mfs := runScrape(t, c)

	if v, _ := gaugeVal(mfs, "netrtmp_up", nil); v != 0 {
		t.Errorf("netrtmp_up = %v, want 0 on ss error", v)
	}
	if v, _ := gaugeVal(mfs, "netrtmp_conntrack_up", nil); v != 1 {
		t.Errorf("netrtmp_conntrack_up = %v, want 1 (conntrack still ok)", v)
	}
	if v, ok := counterVal(mfs, "netrtmp_scrape_errors_total", map[string]string{"source": "ss"}); !ok || v != 1 {
		t.Errorf("scrape_errors{ss} = %v (ok=%v), want 1", v, ok)
	}
}

func TestCollectorConntrackError(t *testing.T) {
	var (
		ssConns []ss.Connection
		ctFlows []conntrack.Flow
		ssErr   error
		ctErr   error = errors.New("conntrack boom")
	)
	c := newFakeCollector(&ssConns, &ctFlows, &ssErr, &ctErr)
	mfs := runScrape(t, c)

	if v, _ := gaugeVal(mfs, "netrtmp_up", nil); v != 1 {
		t.Errorf("netrtmp_up = %v, want 1 (ss still ok)", v)
	}
	if v, _ := gaugeVal(mfs, "netrtmp_conntrack_up", nil); v != 0 {
		t.Errorf("netrtmp_conntrack_up = %v, want 0 on conntrack error", v)
	}
	if v, ok := counterVal(mfs, "netrtmp_scrape_errors_total", map[string]string{"source": "conntrack"}); !ok || v != 1 {
		t.Errorf("scrape_errors{conntrack} = %v (ok=%v), want 1", v, ok)
	}
}

// TestCollectorScrapeErrorsPreinitialized verifies review issue #1: the
// scrape_errors_total series appear even on a healthy host with zero errors.
func TestCollectorScrapeErrorsPreinitialized(t *testing.T) {
	var (
		ssConns []ss.Connection
		ctFlows []conntrack.Flow
		ssErr   error
		ctErr   error
	)
	c := newFakeCollector(&ssConns, &ctFlows, &ssErr, &ctErr)
	mfs := runScrape(t, c)

	if v, ok := counterVal(mfs, "netrtmp_scrape_errors_total", map[string]string{"source": "ss"}); !ok {
		t.Error("scrape_errors_total{source=\"ss\"} should exist even at 0")
	} else if v != 0 {
		t.Errorf("scrape_errors{ss} = %v, want 0", v)
	}
	if v, ok := counterVal(mfs, "netrtmp_scrape_errors_total", map[string]string{"source": "conntrack"}); !ok {
		t.Error("scrape_errors_total{source=\"conntrack\"} should exist even at 0")
	} else if v != 0 {
		t.Errorf("scrape_errors{conntrack} = %v, want 0", v)
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
