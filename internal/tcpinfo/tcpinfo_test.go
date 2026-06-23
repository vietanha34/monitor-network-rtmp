package tcpinfo

import "testing"

// Real-world `ss -H -t -n -i state established` sample (kernel 4.6+),
// adapted from a production host. Each connection is two lines: a connection
// line and an indented TCP_INFO line.
const sample = "ESTAB                            0                            0                                                            10.10.2.218:44802                                                     103.90.222.4:1935\n" +
	"         cubic wscale:9,9 rto:240 rtt:36.311/5.053 ato:40 mss:1448 pmtu:1500 rcvmss:1448 advmss:1448 cwnd:270 ssthresh:258 bytes_sent:18048910687 bytes_retrans:881724 bytes_acked:18048020268 bytes_received:226427 segs_out:14216750 segs_in:2651858 data_segs_out:14206730 data_segs_in:13922 send 86.1Mbps lastsnd:20 lastrcv:5476 lastack:92 pacing_rate 103Mbps delivery_rate 4.5Mbps delivered:14206724 app_limited busy:3564984ms unacked:7 retrans:0/1301 dsack_dups:1301 rcv_rtt:247223 rcv_space:45564 rcv_ssthresh:42242 notsent:981 minrtt:0.247\n" +
	"ESTAB                            0                            0                                                          10.10.2.218:36646                                                    103.90.222.14:1935\n" +
	"         cubic wscale:11,9 rto:204 rtt:0.337/0.098 ato:40 mss:1448 pmtu:1500 rcvmss:1448 advmss:1448 cwnd:321 ssthresh:319 bytes_sent:7986938984 bytes_retrans:28165 bytes_acked:7986910820 bytes_received:3640 segs_out:6073968 segs_in:1287616 data_segs_out:6073963 data_segs_in:8 send 11Gbps lastsnd:8 lastrcv:15210948 lastack:8 pacing_rate 13.2Gbps delivery_rate 39.4Mbps delivered:6073964 busy:218132ms retrans:0/30 dsack_dups:30 reordering:134 reord_seen:5607 rcv_space:14480 rcv_ssthresh:42242 minrtt:0.211\n" +
	"ESTAB                            0                            0                                                            127.0.0.1:58606                                                        127.0.0.1:31729\n" +
	"         cubic wscale:9,9 rto:216 rtt:12.271/18.459 ato:40 mss:65483 pmtu:65535 rcvmss:536 advmss:65483 cwnd:10 bytes_sent:8128131 bytes_acked:8128132 bytes_received:57975 segs_out:92832 segs_in:92831 data_segs_out:34858 data_segs_in:57972 send 427Mbps lastsnd:264 lastrcv:264 lastack:264 pacing_rate 854Mbps delivery_rate 131Gbps delivered:34859 app_limited busy:528632ms rcv_rtt:345894 rcv_space:44031 rcv_ssthresh:43690 minrtt:0.004\n"

func TestParseLines(t *testing.T) {
	flows, err := parseLines([]byte(sample), 1935)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Only two of the three sockets have dport=1935; the 127.0.0.1:31729 one is filtered.
	if len(flows) != 2 {
		t.Fatalf("got %d flows, want 2: %+v", len(flows), flows)
	}

	// First flow: 10.10.2.218:44802 -> 103.90.222.4:1935
	f0 := flows[0]
	if f0.LocalIP != "10.10.2.218" || f0.LocalPort != 44802 {
		t.Errorf("f0 local = %s:%d, want 10.10.2.218:44802", f0.LocalIP, f0.LocalPort)
	}
	if f0.DestIP != "103.90.222.4" || f0.DestPort != 1935 {
		t.Errorf("f0 dest = %s:%d, want 103.90.222.4:1935", f0.DestIP, f0.DestPort)
	}
	if f0.SentBytes != 18048910687 {
		t.Errorf("f0 sent bytes = %d, want 18048910687", f0.SentBytes)
	}
	if f0.RecvBytes != 226427 {
		t.Errorf("f0 recv bytes = %d, want 226427", f0.RecvBytes)
	}
	if f0.SentPkts != 14216750 {
		t.Errorf("f0 segs_out = %d, want 14216750", f0.SentPkts)
	}
	if f0.RecvPkts != 2651858 {
		t.Errorf("f0 segs_in = %d, want 2651858", f0.RecvPkts)
	}

	// Second flow: 10.10.2.218:36646 -> 103.90.222.14:1935
	f1 := flows[1]
	if f1.DestIP != "103.90.222.14" || f1.LocalPort != 36646 {
		t.Errorf("f1 = %s:%d -> %s, want 10.10.2.218:36646 -> 103.90.222.14", f1.LocalIP, f1.LocalPort, f1.DestIP)
	}
	if f1.SentBytes != 7986938984 || f1.RecvBytes != 3640 {
		t.Errorf("f1 bytes sent=%d recv=%d, want 7986938984/3640", f1.SentBytes, f1.RecvBytes)
	}
}

func TestParseLinesEmpty(t *testing.T) {
	flows, err := parseLines(nil, 1935)
	if err != nil {
		t.Fatalf("empty input should not error: %v", err)
	}
	if len(flows) != 0 {
		t.Errorf("got %d flows, want 0", len(flows))
	}
}

func TestParseLinesNoMatchingPort(t *testing.T) {
	// Connections exist but none match port 9999. Should return 0 flows and
	// NO error (kernel supports TCP_INFO, just no matching connections).
	flows, err := parseLines([]byte(sample), 9999)
	if err != nil {
		t.Fatalf("no-match port should not error: %v", err)
	}
	if len(flows) != 0 {
		t.Errorf("got %d flows, want 0", len(flows))
	}
}

func TestParseLinesUnsupportedKernel(t *testing.T) {
	// Connection lines present but no bytes_sent field anywhere -> old kernel.
	input := []byte("ESTAB  0  0  10.0.0.5:38211  93.184.216.34:1935\n" +
		"         cubic wscale:9,9 rto:216 rtt:12.271/18.459 ato:40 mss:65483 cwnd:10 lastsnd:264 lastrcv:264\n" +
		"ESTAB  0  0  10.0.0.5:38212  93.184.216.34:1935\n" +
		"         cubic wscale:9,9 rto:204 rtt:0.337/0.098 ato:40 cwnd:321\n")
	flows, err := parseLines(input, 1935)
	if err == nil {
		t.Fatalf("expected unsupported-kernel error, got flows=%+v", flows)
	}
}

func TestParseLinesConnectionWithoutInfoLine(t *testing.T) {
	// A connection line with no following TCP_INFO line. On a real system
	// `ss -i` always prints an info line per socket, so this situation
	// indicates an old kernel that lacks TCP_INFO byte fields — which the
	// parser must report as an error so auto mode falls back to conntrack.
	input := []byte("ESTAB  0  0  10.0.0.5:38211  93.184.216.34:1935\n")
	flows, err := parseLines(input, 1935)
	if err == nil {
		t.Fatalf("expected unsupported-kernel error, got flows=%+v", flows)
	}
}

func TestParseLinesIPv6(t *testing.T) {
	input := []byte("ESTAB  0  0  [2001:db8::1]:51234  [2001:db8::2]:1935\n" +
		"         cubic wscale:9,9 rto:240 bytes_sent:12345 bytes_received:6789 segs_out:100 segs_in:90 send 10Mbps\n")
	flows, err := parseLines(input, 1935)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(flows) != 1 {
		t.Fatalf("got %d flows, want 1", len(flows))
	}
	f := flows[0]
	if f.LocalIP != "2001:db8::1" || f.LocalPort != 51234 || f.DestIP != "2001:db8::2" || f.DestPort != 1935 {
		t.Errorf("ipv6 flow = %+v, want 2001:db8::1:51234 -> 2001:db8::2:1935", f)
	}
	if f.SentBytes != 12345 || f.RecvBytes != 6789 || f.SentPkts != 100 || f.RecvPkts != 90 {
		t.Errorf("ipv6 counters = sent=%d recv=%d pkts=%d/%d, want 12345/6789/100/90",
			f.SentBytes, f.RecvBytes, f.SentPkts, f.RecvPkts)
	}
}
