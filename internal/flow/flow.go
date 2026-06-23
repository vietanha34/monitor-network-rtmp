// Package flow defines the shared Flow type representing a single monitored
// outbound TCP connection with byte/packet counters. It is produced by either
// the conntrack byte source or the ss TCP_INFO (tcpinfo) byte source and
// consumed by the collector.
package flow

// Flow is a single monitored outbound TCP connection with per-direction
// byte and packet counters from the active byte source.
type Flow struct {
	LocalIP   string
	LocalPort int
	DestIP    string
	DestPort  int
	SentBytes uint64
	SentPkts  uint64
	RecvBytes uint64
	RecvPkts  uint64
}
