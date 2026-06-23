// Package collector orchestrates ss + a byte source (conntrack or ss TCP_INFO)
// on each Prometheus scrape and exposes metrics. It maintains an internal
// accumulator so that netrtmp_bytes_total is a monotonic counter even though
// per-flow counters reset when a flow disappears.
package collector

import (
	"context"
	"log"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/vietanha34/monitor-network-rtmp/internal/config"
	"github.com/vietanha34/monitor-network-rtmp/internal/conntrack"
	"github.com/vietanha34/monitor-network-rtmp/internal/flow"
	"github.com/vietanha34/monitor-network-rtmp/internal/ss"
	"github.com/vietanha34/monitor-network-rtmp/internal/tcpinfo"
)

const namespace = "netrtmp"

// Collector implements prometheus.Collector.
type Collector struct {
	ssPath        string
	conntrackPath string
	targetPort    int
	scrapeTimeout time.Duration

	// byteSource is the configured byte-counter source (auto|conntrack|ss-tcpinfo).
	byteSource string
	// resolvedSource is the byte source auto mode locked in after the first
	// successful probe. Empty until resolved.
	resolvedSource string

	// ssList / flowList are the data sources. They default to the real
	// commands but can be replaced in tests. flowList returns byte counters
	// from the active byte source (conntrack or tcpinfo).
	ssList      ssListFunc
	flowList    flowListFunc
	tcpinfoList flowListFunc // used by the auto-mode probe; defaults to tcpinfo.List

	up             prometheus.Gauge
	byteSourceUp   prometheus.Gauge
	scrapeDuration prometheus.Gauge
	scrapeErrors   *prometheus.CounterVec
	connActive     *prometheus.GaugeVec
	bytesTotal     *prometheus.CounterVec
	connBytes      *prometheus.GaugeVec
	connPackets    *prometheus.GaugeVec

	mu sync.Mutex

	// lastRaw stores the last raw per-flow byte counter seen from the byte
	// source, keyed by (dest, destPort, localPort, direction). Used to
	// compute deltas for the monotonic aggregate counter.
	lastRaw map[flowKey]uint64
	// lastDestLabels / lastConnLabels remember which gauge label sets were
	// emitted last scrape so stale ones can be deleted (bounds cardinality).
	lastDestLabels map[destLabel]struct{}
	lastConnLabels map[connLabel]struct{}
}

type ssListFunc func(ctx context.Context, ssPath string, targetPort int) ([]ss.Connection, error)
type flowListFunc func(ctx context.Context, binPath string, targetPort int) ([]flow.Flow, error)

type flowKey struct {
	destIP    string
	destPort  int
	localPort int
	dir       string
}

type flowLookup struct {
	localIP   string
	localPort int
	destIP    string
	destPort  int
}

type destLabel struct {
	destIP   string
	destPort int
}

type connLabel struct {
	destIP    string
	destPort  int
	localPort int
}

type aggKey struct {
	destIP   string
	destPort int
	dir      string
}

// New creates a new Collector. byteSource is one of config.ByteSource*.
func New(ssPath, conntrackPath string, targetPort int, scrapeTimeout time.Duration, byteSource string) *Collector {
	c := &Collector{
		ssPath:        ssPath,
		conntrackPath: conntrackPath,
		targetPort:    targetPort,
		scrapeTimeout: scrapeTimeout,
		byteSource:    byteSource,
		ssList:       ss.List,
		tcpinfoList:  tcpinfo.List,

		up: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "up",
			Help:      "1 if the ss connection scrape succeeded, 0 otherwise.",
		}),
		byteSourceUp: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "byte_source_up",
			Help:      "1 if the byte-source scrape (conntrack or ss-tcpinfo) succeeded, 0 otherwise.",
		}),
		scrapeDuration: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "scrape_duration_seconds",
			Help:      "Duration of the last scrape in seconds.",
		}),
		scrapeErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "scrape_errors_total",
			Help:      "Total number of scrape errors by source.",
		}, []string{"source"}),
		connActive: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "connections_active",
			Help:      "Number of established outbound TCP connections to the target port.",
		}, []string{"dest_ip", "dest_port"}),
		bytesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "bytes_total",
			Help:      "Total bytes transferred over established outbound connections to the target port (monotonic; survives per-flow resets).",
		}, []string{"dest_ip", "dest_port", "direction"}),
		connBytes: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "connection_bytes",
			Help:      "Current byte counter for an individual established connection from the active byte source (resets when the connection closes).",
		}, []string{"dest_ip", "dest_port", "local_port", "direction"}),
		connPackets: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "connection_packets",
			Help:      "Current packet counter for an individual established connection from the active byte source.",
		}, []string{"dest_ip", "dest_port", "local_port", "direction"}),

		lastRaw:        map[flowKey]uint64{},
		lastDestLabels: map[destLabel]struct{}{},
		lastConnLabels: map[connLabel]struct{}{},
	}

	// Wire the byte source. For "auto", flowList defaults to conntrack.List
	// as the fallback (used after the ss-tcpinfo probe fails in Collect); the
	// probe itself uses c.tcpinfoList. For explicit modes, wire directly.
	switch byteSource {
	case config.ByteSourceConntrack:
		c.flowList = conntrack.List
	case config.ByteSourceTCPInfo:
		c.flowList = tcpinfo.List
	default: // auto or empty
		c.flowList = conntrack.List
	}

	// Pre-create the scrape_errors_total label sets so the series appear even
	// on a healthy host that has never had a scrape error.
	c.scrapeErrors.WithLabelValues("ss").Add(0)
	c.scrapeErrors.WithLabelValues(conntrack.SourceName).Add(0)
	c.scrapeErrors.WithLabelValues(tcpinfo.SourceName).Add(0)
	return c
}

// Describe implements prometheus.Collector.
func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	c.up.Describe(ch)
	c.byteSourceUp.Describe(ch)
	c.scrapeDuration.Describe(ch)
	c.scrapeErrors.Describe(ch)
	c.connActive.Describe(ch)
	c.bytesTotal.Describe(ch)
	c.connBytes.Describe(ch)
	c.connPackets.Describe(ch)
}

// Collect implements prometheus.Collector. It resolves the byte source (for
// auto mode), runs ss and the byte source concurrently, computes metrics,
// and emits them.
func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), c.scrapeTimeout)
	defer cancel()

	ssFn := c.ssList
	var flowFn flowListFunc
	var sourceLabel string

	switch c.byteSource {
	case config.ByteSourceConntrack:
		flowFn = c.flowList
		sourceLabel = conntrack.SourceName
	case config.ByteSourceTCPInfo:
		flowFn = c.flowList
		sourceLabel = tcpinfo.SourceName
	default: // auto
		if c.resolvedSource == "" {
			// First scrape: probe ss-tcpinfo. If it is unsupported (old kernel)
			// or unavailable, fall back to conntrack. Reuse the probe result
			// for this scrape to avoid a second invocation.
			f, err := c.tcpinfoList(ctx, c.ssPath, c.targetPort)
			if err == nil {
				c.resolvedSource = config.ByteSourceTCPInfo
				sourceLabel = tcpinfo.SourceName
				c.runScrape(ctx, ch, start, ssFn, func(context.Context, string, int) ([]flow.Flow, error) {
					return f, nil
				}, sourceLabel)
				return
			}
			// One-time warning so the operator knows why auto mode is not using
			// ss-tcpinfo (e.g. old kernel). Logged once, not every scrape.
			log.Printf("byte source: ss-tcpinfo unavailable (%v); falling back to conntrack for this session", err)
			c.resolvedSource = config.ByteSourceConntrack
		}
		if c.resolvedSource == config.ByteSourceTCPInfo {
			flowFn = c.tcpinfoList
			sourceLabel = tcpinfo.SourceName
		} else {
			flowFn = c.flowList
			sourceLabel = conntrack.SourceName
		}
	}

	c.runScrape(ctx, ch, start, ssFn, flowFn, sourceLabel)
}

// runScrape executes the ss and byte-source functions concurrently, then
// computes and emits all metrics under c.mu.
func (c *Collector) runScrape(ctx context.Context, ch chan<- prometheus.Metric, start time.Time, ssFn ssListFunc, flowFn flowListFunc, sourceLabel string) {
	var (
		ssConns  []ss.Connection
		flows    []flow.Flow
		ssErr    error
		flowErr  error
		wg       sync.WaitGroup
	)
	wg.Add(2)
	go func() { defer wg.Done(); ssConns, ssErr = ssFn(ctx, c.ssPath, c.targetPort) }()
	// flowFn uses the ss binary for tcpinfo mode and the conntrack binary for
	// conntrack mode; pass the appropriate path.
	binPath := c.conntrackPath
	if sourceLabel == tcpinfo.SourceName {
		binPath = c.ssPath
	}
	go func() { defer wg.Done(); flows, flowErr = flowFn(ctx, binPath, c.targetPort) }()
	wg.Wait()

	// Prometheus calls Collect single-threaded (one scrape at a time), so the
	// wide scope of c.mu is intentional: it guards the accumulator + stale-
	// label bookkeeping across the whole post-scrape computation. We keep it
	// for safety in case the collector is ever shared across registries.
	c.mu.Lock()
	defer c.mu.Unlock()

	// Health signals.
	if ssErr != nil {
		c.scrapeErrors.WithLabelValues("ss").Inc()
		c.up.Set(0)
	} else {
		c.up.Set(1)
	}
	if flowErr != nil {
		c.scrapeErrors.WithLabelValues(sourceLabel).Inc()
		c.byteSourceUp.Set(0)
	} else {
		c.byteSourceUp.Set(1)
	}

	// Index byte-source flows by 4-tuple for O(1) lookup from ss connections.
	flowMap := make(map[flowLookup]flow.Flow, len(flows))
	for _, f := range flows {
		flowMap[flowLookup{f.LocalIP, f.LocalPort, f.DestIP, f.DestPort}] = f
	}

	curDest := map[destLabel]struct{}{}
	curConn := map[connLabel]struct{}{}
	activeCount := map[destLabel]int{}
	aggDelta := map[aggKey]uint64{}

	dirs := [2]string{"sent", "received"}

	for _, conn := range ssConns {
		dl := destLabel{conn.DestIP, conn.DestPort}
		activeCount[dl]++
		curDest[dl] = struct{}{}

		f, ok := flowMap[flowLookup{conn.LocalIP, conn.LocalPort, conn.DestIP, conn.DestPort}]
		if !ok {
			continue
		}

		cl := connLabel{conn.DestIP, conn.DestPort, conn.LocalPort}
		curConn[cl] = struct{}{}

		dp := strconv.Itoa(conn.DestPort)
		lp := strconv.Itoa(conn.LocalPort)
		c.connBytes.WithLabelValues(conn.DestIP, dp, lp, "sent").Set(float64(f.SentBytes))
		c.connBytes.WithLabelValues(conn.DestIP, dp, lp, "received").Set(float64(f.RecvBytes))
		c.connPackets.WithLabelValues(conn.DestIP, dp, lp, "sent").Set(float64(f.SentPkts))
		c.connPackets.WithLabelValues(conn.DestIP, dp, lp, "received").Set(float64(f.RecvPkts))

		// Compute monotonic deltas for the aggregate counter.
		raws := [2]uint64{f.SentBytes, f.RecvBytes}
		for i, dir := range dirs {
			fk := flowKey{conn.DestIP, conn.DestPort, conn.LocalPort, dir}
			last := c.lastRaw[fk]
			raw := raws[i]
			var delta uint64
			if raw >= last {
				delta = raw - last
			} else {
				// Flow reset / reuse: treat current value as fresh.
				delta = raw
			}
			c.lastRaw[fk] = raw
			if delta > 0 {
				aggDelta[aggKey{conn.DestIP, conn.DestPort, dir}] += delta
			}
		}
	}

	// Apply aggregate counter deltas.
	for k, d := range aggDelta {
		if d > 0 {
			c.bytesTotal.WithLabelValues(k.destIP, strconv.Itoa(k.destPort), k.dir).Add(float64(d))
		}
	}

	// Active connections per dest.
	for dl, n := range activeCount {
		c.connActive.WithLabelValues(dl.destIP, strconv.Itoa(dl.destPort)).Set(float64(n))
	}

	// Delete stale dest gauges (dests with no connections this scrape).
	for dl := range c.lastDestLabels {
		if _, ok := curDest[dl]; !ok {
			c.connActive.DeleteLabelValues(dl.destIP, strconv.Itoa(dl.destPort))
		}
	}
	c.lastDestLabels = curDest

	// Delete stale per-connection gauges (both directions).
	for cl := range c.lastConnLabels {
		if _, ok := curConn[cl]; !ok {
			dp := strconv.Itoa(cl.destPort)
			lp := strconv.Itoa(cl.localPort)
			c.connBytes.DeleteLabelValues(cl.destIP, dp, lp, "sent")
			c.connBytes.DeleteLabelValues(cl.destIP, dp, lp, "received")
			c.connPackets.DeleteLabelValues(cl.destIP, dp, lp, "sent")
			c.connPackets.DeleteLabelValues(cl.destIP, dp, lp, "received")
		}
	}
	c.lastConnLabels = curConn

	// Bound lastRaw memory: drop entries for flows no longer present.
	present := make(map[flowKey]struct{}, len(ssConns)*2)
	for _, conn := range ssConns {
		for _, dir := range dirs {
			present[flowKey{conn.DestIP, conn.DestPort, conn.LocalPort, dir}] = struct{}{}
		}
	}
	for fk := range c.lastRaw {
		if _, ok := present[fk]; !ok {
			delete(c.lastRaw, fk)
		}
	}

	c.scrapeDuration.Set(time.Since(start).Seconds())

	// Emit all metrics.
	c.up.Collect(ch)
	c.byteSourceUp.Collect(ch)
	c.scrapeDuration.Collect(ch)
	c.scrapeErrors.Collect(ch)
	c.connActive.Collect(ch)
	c.bytesTotal.Collect(ch)
	c.connBytes.Collect(ch)
	c.connPackets.Collect(ch)
}

// ResolvedSource returns the byte source that auto mode locked in (one of
// config.ByteSourceConntrack / config.ByteSourceTCPInfo). For explicit modes
// it returns the configured value. Intended for logging / diagnostics.
func (c *Collector) ResolvedSource() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.resolvedSource != "" {
		return c.resolvedSource
	}
	return c.byteSource
}
