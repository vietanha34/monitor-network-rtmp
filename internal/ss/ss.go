// Package ss parses output of the `ss` command to list established
// outbound TCP connections to a given target port.
package ss

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// Connection is a single established outbound TCP connection.
type Connection struct {
	LocalIP   string
	LocalPort int
	DestIP    string
	DestPort  int
	// PID is the owning process ID, or "0" when ss cannot determine it
	// (e.g. the socket belongs to another user and the exporter is not
	// running as root). "0" is used instead of "" so the label value is
	// always a valid non-empty Prometheus label.
	PID string
}

// pidRe extracts the process id from ss process info like
// users:(("ffmpeg",pid=12345,fd=6)).
var pidRe = regexp.MustCompile(`pid=(\d+)`)

// List runs `ss -H -t -n -p state established` and returns connections
// whose peer (destination) port equals targetPort. The -p flag requests
// process info so the owning PID can be captured; it requires root (or
// CAP_NET_ADMIN) to see PIDs of sockets owned by other users.
func List(ctx context.Context, ssPath string, targetPort int) ([]Connection, error) {
	cmd := exec.CommandContext(ctx, ssPath, "-H", "-t", "-n", "-p", "state", "established")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("run ss: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return parseLines(out, targetPort), nil
}

// parseLines parses raw `ss` output and returns connections whose peer
// (destination) port equals targetPort. Extracted for unit testing.
func parseLines(out []byte, targetPort int) []Connection {
	var conns []Connection
	for _, line := range bytes.Split(out, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		localIP, localPort, destIP, destPort, ok := ParseConnFields(string(line))
		if !ok || destPort != targetPort {
			continue
		}
		conns = append(conns, Connection{
			LocalIP:   localIP,
			LocalPort: localPort,
			DestIP:    destIP,
			DestPort:  destPort,
			PID:       parsePID(line),
		})
	}
	return conns
}

// parsePID extracts the owning process id from a single ss line, returning
// "0" when no pid= field is present (socket owned by another user / no -p).
func parsePID(line []byte) string {
	if m := pidRe.FindSubmatch(line); m != nil {
		return string(m[1])
	}
	return "0"
}

// ParseConnFields extracts the local and peer address:port from a single `ss`
// line's whitespace-separated fields. It is robust to the optional leading
// State column (e.g. "ESTAB") and trailing Process columns: it scans for the
// first two fields that parse as addr:port. Exported so the tcpinfo package
// reuses the same logic.
func ParseConnFields(line string) (localIP string, localPort int, destIP string, destPort int, ok bool) {
	fields := strings.Fields(line)
	found := 0
	for _, f := range fields {
		ip, port, parsed := SplitAddrPort(f)
		if !parsed {
			continue
		}
		if found == 0 {
			localIP, localPort = ip, port
			found++
		} else {
			destIP, destPort = ip, port
			found++
			break
		}
	}
	if found < 2 {
		return "", 0, "", 0, false
	}
	return localIP, localPort, destIP, destPort, true
}

// SplitAddrPort splits "ip:port" or "[ipv6]:port" into its parts.
// Exported so the tcpinfo package can reuse the same address parsing.
func SplitAddrPort(s string) (string, int, bool) {
	if strings.HasPrefix(s, "[") {
		idx := strings.Index(s, "]:")
		if idx < 0 {
			return "", 0, false
		}
		ip := s[1:idx]
		port, err := strconv.Atoi(s[idx+2:])
		if err != nil {
			return "", 0, false
		}
		return ip, port, true
	}
	idx := strings.LastIndex(s, ":")
	if idx < 0 {
		return "", 0, false
	}
	ip := s[:idx]
	port, err := strconv.Atoi(s[idx+1:])
	if err != nil {
		return "", 0, false
	}
	return ip, port, true
}
