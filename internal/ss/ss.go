// Package ss parses output of the `ss` command to list established
// outbound TCP connections to a given target port.
package ss

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Connection is a single established outbound TCP connection.
type Connection struct {
	LocalIP   string
	LocalPort int
	DestIP    string
	DestPort  int
}

// List runs `ss -H -t -n state established` and returns connections
// whose peer (destination) port equals targetPort.
func List(ctx context.Context, ssPath string, targetPort int) ([]Connection, error) {
	cmd := exec.CommandContext(ctx, ssPath, "-H", "-t", "-n", "state", "established")
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
		// Fields: Recv-Q Send-Q LocalAddr:Port PeerAddr:Port [Process...]
		fields := strings.Fields(string(line))
		if len(fields) < 4 {
			continue
		}
		localIP, localPort, okL := splitAddrPort(fields[2])
		destIP, destPort, okD := splitAddrPort(fields[3])
		if !okL || !okD {
			continue
		}
		if destPort != targetPort {
			continue
		}
		conns = append(conns, Connection{
			LocalIP:   localIP,
			LocalPort: localPort,
			DestIP:    destIP,
			DestPort:  destPort,
		})
	}
	return conns
}

// splitAddrPort splits "ip:port" or "[ipv6]:port" into its parts.
func splitAddrPort(s string) (string, int, bool) {
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
