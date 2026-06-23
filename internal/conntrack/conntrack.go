// Package conntrack parses output of `conntrack -L` to obtain per-flow
// byte/packet counters for outbound TCP connections to a target port.
package conntrack

import (
	"bytes"
	"context"
	"errors"
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
		// Some conntrack versions exit non-zero (e.g. code 1) when no flows
		// match the filter, but still print the informational
		// "N flow entries have been shown." line to stderr. That is an empty
		// table, NOT a failure — treat it as such so netrtmp_conntrack_up stays
		// 1 on a host with no RTMP connections. A missing binary or a real
		// error (permission denied, module not loaded) is surfaced separately:
		// a missing binary is not an *exec.ExitError, and real errors print an
		// error message to stderr rather than the informational line.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			stderrStr := strings.TrimSpace(stderr.String())
			if len(out) == 0 && (stderrStr == "" || strings.Contains(stderrStr, "have been shown")) {
				return nil, nil
			}
		}
		return nil, fmt.Errorf("run conntrack: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return parseLines(out, targetPort), nil
}

// parseLines parses raw `conntrack -L` output and returns flows whose
// original-direction dport equals targetPort. Extracted for unit testing.
func parseLines(out []byte, targetPort int) []Flow {
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
	return flows
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
