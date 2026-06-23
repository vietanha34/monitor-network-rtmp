// Package conntrack parses output of `conntrack -L` to obtain per-flow
// byte/packet counters for outbound TCP connections to a target port.
package conntrack

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// Flow is a single conntrack flow (original direction = outbound).
type Flow struct {
	LocalIP    string
	LocalPort  int
	DestIP     string
	DestPort   int
	SentBytes  uint64
	SentPkts   uint64
	RecvBytes  uint64
	RecvPkts   uint64
}

var (
	// Matches "src=A dst=B sport=N dport=M". Two occurrences per line:
	// the original tuple (outbound) and the reply tuple (inbound).
	tupleRe = regexp.MustCompile(`src=(\S+)\s+dst=(\S+)\s+sport=(\d+)\s+dport=(\d+)`)
	// Matches "packets=N bytes=M". Two occurrences per line.
	counterRe = regexp.MustCompile(`packets=(\d+)\s+bytes=(\d+)`)
)

// List runs `conntrack -L -p tcp --dport <targetPort>` and returns flows
// whose original-direction dport equals targetPort.
func List(ctx context.Context, conntrackPath string, targetPort int) ([]Flow, error) {
	cmd := exec.CommandContext(ctx, conntrackPath,
		"-L", "-p", "tcp", "--dport", strconv.Itoa(targetPort))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("run conntrack: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	var flows []Flow
	for _, line := range bytes.Split(out, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		f, ok := parseLine(line, targetPort)
		if !ok {
			continue
		}
		flows = append(flows, f)
	}
	return flows, nil
}

func parseLine(line []byte, targetPort int) (Flow, bool) {
	tuples := tupleRe.FindAllSubmatch(line, -1)
	if len(tuples) < 2 {
		return Flow{}, false
	}
	orig := tuples[0] // original (outbound) tuple

	localPort, _ := strconv.Atoi(string(orig[3]))
	destPort, _ := strconv.Atoi(string(orig[4]))
	if destPort != targetPort {
		return Flow{}, false
	}

	f := Flow{
		LocalIP:   string(orig[1]),
		LocalPort: localPort,
		DestIP:    string(orig[2]),
		DestPort:  destPort,
	}

	counters := counterRe.FindAllSubmatch(line, -1)
	if len(counters) >= 2 {
		// First counter group belongs to the original tuple (bytes we sent).
		f.SentPkts, _ = strconv.ParseUint(string(counters[0][1]), 10, 64)
		f.SentBytes, _ = strconv.ParseUint(string(counters[0][2]), 10, 64)
		// Second counter group belongs to the reply tuple (bytes we received).
		f.RecvPkts, _ = strconv.ParseUint(string(counters[1][1]), 10, 64)
		f.RecvBytes, _ = strconv.ParseUint(string(counters[1][2]), 10, 64)
	}
	return f, true
}
