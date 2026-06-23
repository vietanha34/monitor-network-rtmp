package conntrack

import "testing"

const sampleLine = "tcp      6 431998 ESTABLISHED src=10.0.0.5 dst=93.184.216.34 sport=38211 dport=1935 packets=120 bytes=24000 src=93.184.216.34 dst=10.0.0.5 sport=1935 dport=38211 packets=80 bytes=16000 [ASSURED] mark=0 use=1"

func TestParseLine(t *testing.T) {
	f, ok := parseLine([]byte(sampleLine), 1935)
	if !ok {
		t.Fatal("expected ok, got false")
	}
	if f.LocalIP != "10.0.0.5" || f.LocalPort != 38211 {
		t.Errorf("local = %s:%d, want 10.0.0.5:38211", f.LocalIP, f.LocalPort)
	}
	if f.DestIP != "93.184.216.34" || f.DestPort != 1935 {
		t.Errorf("dest = %s:%d, want 93.184.216.34:1935", f.DestIP, f.DestPort)
	}
	if f.SentBytes != 24000 || f.SentPkts != 120 {
		t.Errorf("sent = bytes=%d pkts=%d, want 24000/120", f.SentBytes, f.SentPkts)
	}
	if f.RecvBytes != 16000 || f.RecvPkts != 80 {
		t.Errorf("recv = bytes=%d pkts=%d, want 16000/80", f.RecvBytes, f.RecvPkts)
	}
}

func TestParseLineWrongPort(t *testing.T) {
	if _, ok := parseLine([]byte(sampleLine), 443); ok {
		t.Fatal("expected ok=false for non-matching target port")
	}
}

func TestParseLineNoCounters(t *testing.T) {
	line := []byte("tcp      6 431998 ESTABLISHED src=10.0.0.5 dst=93.184.216.34 sport=38211 dport=1935 src=93.184.216.34 dst=10.0.0.5 sport=1935 dport=38211 [ASSURED] mark=0 use=1")
	f, ok := parseLine(line, 1935)
	if !ok {
		t.Fatal("expected ok even without counters")
	}
	if f.SentBytes != 0 || f.RecvBytes != 0 || f.SentPkts != 0 || f.RecvPkts != 0 {
		t.Errorf("expected zero counters, got sent=%d/%d recv=%d/%d",
			f.SentBytes, f.SentPkts, f.RecvBytes, f.RecvPkts)
	}
}

func TestParseLineMalformed(t *testing.T) {
	cases := [][]byte{
		[]byte("garbage no tuples here"),
		[]byte("tcp only one tuple src=10.0.0.5 dst=1.2.3.4 sport=5 dport=1935"),
		nil,
	}
	for _, line := range cases {
		if _, ok := parseLine(line, 1935); ok {
			t.Errorf("expected ok=false for malformed line %q", line)
		}
	}
}

func TestParseLinesMultiple(t *testing.T) {
	input := []byte(sampleLine + "\n" +
		"tcp      6 431998 ESTABLISHED src=10.0.0.5 dst=93.184.216.34 sport=38212 dport=1935 packets=10 bytes=2000 src=93.184.216.34 dst=10.0.0.5 sport=1935 dport=38212 packets=5 bytes=1000 [ASSURED] mark=0 use=1\n" +
		"\n")
	flows := parseLines(input, 1935)
	if len(flows) != 2 {
		t.Fatalf("got %d flows, want 2", len(flows))
	}
	if flows[1].SentBytes != 2000 || flows[1].RecvBytes != 1000 {
		t.Errorf("flow[1] sent=%d recv=%d, want 2000/1000", flows[1].SentBytes, flows[1].RecvBytes)
	}
}

func TestParseLinesEmpty(t *testing.T) {
	if got := parseLines(nil, 1935); len(got) != 0 {
		t.Errorf("parseLines(nil) = %d, want 0", len(got))
	}
}
