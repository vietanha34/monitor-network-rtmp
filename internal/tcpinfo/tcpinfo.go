// Package tcpinfo parses `ss -ti` output (TCP_INFO) to obtain per-connection
// byte/packet counters without requiring conntrack-tools or the nf_conntrack
// kernel module. Requires kernel >= 4.6, which exposes bytes_sent and
// bytes_received in struct tcp_info.
package tcpinfo

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/vietanha34/monitor-network-rtmp/internal/flow"
	"github.com/vietanha34/monitor-network-rtmp/internal/ss"
)

var (
	bytesSentRe = regexp.MustCompile(`bytes_sent:(\d+)`)
	bytesRecvRe = regexp.MustCompile(`bytes_received:(\d+)`)
	segsOutRe   = regexp.MustCompile(`segs_out:(\d+)`)
	segsInRe    = regexp.MustCompile(`segs_in:(\d+)`)
)

// SourceName is the label value used for this byte source in metrics.
const SourceName = "ss-tcpinfo"

// List runs `ss -H -t -n -i state established` and returns one flow per
// established outbound connection whose peer port equals targetPort, with
// byte/packet counters populated from TCP_INFO.
//
// On kernels < 4.6, ss does not expose bytes_sent/bytes_received. If any
// connection lines are present but none carry byte counters, List returns
// an error so callers (e.g. auto mode) can fall back to conntrack.
func List(ctx context.Context, ssPath string, targetPort int) ([]flow.Flow, error) {
	cmd := exec.CommandContext(ctx, ssPath, "-H", "-t", "-n", "-i", "state", "established")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("run ss -ti: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return parseLines(out, targetPort)
}

// parseLines parses raw `ss -ti` output. Extracted for unit testing.
// Output is two lines per socket: a connection line (no leading whitespace)
// followed by an indented TCP_INFO continuation line.
func parseLines(out []byte, targetPort int) ([]flow.Flow, error) {
	var (
		flows         []flow.Flow
		pending       *flow.Flow
		connLines     int
		sawBytesField bool
	)

	flush := func() {
		if pending != nil {
			flows = append(flows, *pending)
			pending = nil
		}
	}

	for _, raw := range bytes.Split(out, []byte("\n")) {
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		isInfo := raw[0] == ' ' || raw[0] == '\t'
		if !isInfo {
			// Connection line.
			flush()
			localIP, localPort, destIP, destPort, ok := ss.ParseConnFields(string(raw))
			if !ok {
				continue
			}
			connLines++
			if destPort != targetPort {
				continue
			}
			pending = &flow.Flow{
				LocalIP:   localIP,
				LocalPort: localPort,
				DestIP:    destIP,
				DestPort:  destPort,
			}
			continue
		}

		// TCP_INFO continuation line.
		if m := bytesSentRe.FindSubmatch(raw); m != nil {
			sawBytesField = true
			if pending != nil {
				pending.SentBytes, _ = strconv.ParseUint(string(m[1]), 10, 64)
			}
		}
		if m := bytesRecvRe.FindSubmatch(raw); m != nil && pending != nil {
			pending.RecvBytes, _ = strconv.ParseUint(string(m[1]), 10, 64)
		}
		if m := segsOutRe.FindSubmatch(raw); m != nil && pending != nil {
			pending.SentPkts, _ = strconv.ParseUint(string(m[1]), 10, 64)
		}
		if m := segsInRe.FindSubmatch(raw); m != nil && pending != nil {
			pending.RecvPkts, _ = strconv.ParseUint(string(m[1]), 10, 64)
		}
		flush()
	}
	flush()

	// If there were established connections but none exposed byte counters,
	// the kernel is too old to support TCP_INFO byte fields — signal the
	// caller (auto mode) to fall back to conntrack.
	if connLines > 0 && !sawBytesField {
		return nil, fmt.Errorf("ss -ti does not expose TCP_INFO byte counters (kernel < 4.6?); use --byte-source=conntrack")
	}
	return flows, nil
}
