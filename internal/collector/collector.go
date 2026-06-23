// Package collector orchestrates ss + conntrack on each Prometheus scrape
// and exposes metrics. It maintains an internal accumulator so that
// netrtmp_bytes_total is a monotonic counter even though conntrack
// per-flow counters reset when a flow disappears.
package collector

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/vietanha34/monitor-network-rtmp/internal/conntrack"
	"github.com/vietanha34/monitor-network-rtmp/internal/ss"
)

const namespace = "netrtmp"

// Collector implements prometheus.Collector.
type Collector struct {
	ssPath        string
	conntrackPath string
	targetPort    int
	scrapeTimeout time.Duration

	// ssList / ctList are the data sources. They default to the real ss and
	// conntrack commands but can be replaced in tests.
	ssList ssListFunc
	ctList ctListFunc

	up             prometheus.Gauge
	conntrackUp    prometheus.Gauge
	scrapeDuration prometheus.Gauge
	scrapeErrors   *prometheus.CounterVec
	connActive     *prometheus.GaugeVec
	bytesTotal     *prometheus.CounterVec
	connBytes      *prometheus.GaugeVec
	connPackets    *prometheus.GaugeVec

	mu sync.Mutex

	// lastRaw stores the last raw per-flow byte counter seen from conntrack,
	// keyed by (dest, destPort, localPort, direction). Used to compute deltas.
	lastRaw map[flowKey]uint64
	// lastDestLabels / lastConnLabels remember which gauge label sets were
	// emitted last scrape so stale ones can be deleted (bounds cardinality).
	lastDestLabels map[destLabel]struct{}
	lastConnLabels map[connLabel]struct{}
}

type ssListFunc func(ctx context.Context, ssPath string, targetPort int) ([]ss.Connection, error)
type ctListFunc func(ctx context.Context, conntrackPath string, targetPort int) ([]conntrack.Flow, error)

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

// New creates a new Collector.
func New(ssPath, conntrackPath string, targetPort int, scrapeTimeout time.Duration) *Collector {
	c := &Collector{
		ssPath:        ssPath,
		conntrackPath: conntrackPath,
		targetPort:    targetPort,
		scrapeTimeout: scrapeTimeout,
		ssList:        ss.List,
		ctList:        conntrack.List,

		up: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "up",
			Help:      "1 if the ss scrape succeeded, 0 otherwise.",
		}),
		conntrackUp: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "conntrack_up",
			Help:      "1 if the conntrack command succeeded, 0 otherwise.",
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
			Help:      "Current conntrack byte counter for an individual established connection (resets when the connection closes).",
		}, []string{"dest_ip", "dest_port", "local_port", "direction"}),
		connPackets: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "connection_packets",
			Help:      "Current conntrack packet counter for an individual established connection.",
		}, []string{"dest_ip", "dest_port", "local_port", "direction"}),

		lastRaw:        map[flowKey]uint64{},
		lastDestLabels: map[destLabel]struct{}{},
		lastConnLabels: map[connLabel]struct{}{},
	}
	// Pre-create the scrape_errors_total label sets so the series appear even
	// on a healthy host that has never had a scrape error.
	c.scrapeErrors.WithLabelValues("ss").Add(0)
	c.scrapeErrors.WithLabelValues("conntrack").Add(0)
	return c
}

// Describe implements prometheus.Collector.
func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	c.up.Describe(ch)
	c.conntrackUp.Describe(ch)
	c.scrapeDuration.Describe(ch)
	c.scrapeErrors.Describe(ch)
	c.connActive.Describe(ch)
	c.bytesTotal.Describe(ch)
	c.connBytes.Describe(ch)
	c.connPackets.Describe(ch)
}

// Collect implements prometheus.Collector. It runs ss and conntrack
// concurrently, computes metrics, and emits them.
func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), c.scrapeTimeout)
	defer cancel()

	var (
		ssConns []ss.Connection
		ctFlows []conntrack.Flow
		ssErr   error
		ctErr   error
		wg      sync.WaitGroup
	)
	wg.Add(2)
	go func() { defer wg.Done(); ssConns, ssErr = c.ssList(ctx, c.ssPath, c.targetPort) }()
	go func() { defer wg.Done(); ctFlows, ctErr = c.ctList(ctx, c.conntrackPath, c.targetPort) }()
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
	if ctErr != nil {
		c.scrapeErrors.WithLabelValues("conntrack").Inc()
		c.conntrackUp.Set(0)
	} else {
		c.conntrackUp.Set(1)
	}

	// Index conntrack flows by 4-tuple for O(1) lookup from ss connections.
	flowMap := make(map[flowLookup]conntrack.Flow, len(ctFlows))
	for _, f := range ctFlows {
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
	c.conntrackUp.Collect(ch)
	c.scrapeDuration.Collect(ch)
	c.scrapeErrors.Collect(ch)
	c.connActive.Collect(ch)
	c.bytesTotal.Collect(ch)
	c.connBytes.Collect(ch)
	c.connPackets.Collect(ch)
}
