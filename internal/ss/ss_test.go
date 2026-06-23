package ss

import "testing"

func TestSplitAddrPort(t *testing.T) {
	cases := []struct {
		in   string
		ip   string
		port int
		ok   bool
	}{
		{"10.0.0.5:38211", "10.0.0.5", 38211, true},
		{"93.184.216.34:1935", "93.184.216.34", 1935, true},
		{"[2001:db8::1]:443", "2001:db8::1", 443, true},
		{"[fe80::1]:1935", "fe80::1", 1935, true},
		{"noport", "", 0, false},
		{"10.0.0.5:", "", 0, false},
		{"10.0.0.5:notanum", "", 0, false},
		{"[badipv6:443", "", 0, false},
	}
	for _, tc := range cases {
		ip, port, ok := SplitAddrPort(tc.in)
		if ip != tc.ip || port != tc.port || ok != tc.ok {
			t.Errorf("SplitAddrPort(%q) = (%q, %d, %v), want (%q, %d, %v)",
				tc.in, ip, port, ok, tc.ip, tc.port, tc.ok)
		}
	}
}

func TestParseLines(t *testing.T) {
	input := []byte("0      0      10.0.0.5:38211    93.184.216.34:1935\n" +
		"0      0      10.0.0.5:38212    93.184.216.34:1935\n" +
		"0      0      10.0.0.5:41983    198.51.100.7:443\n" + // wrong port, filtered
		"0      0      [2001:db8::1]:51234    [2001:db8::2]:1935\n" + // ipv6, match
		"\n" +
		"garbage line\n" +
		"0 0 too:short\n")
	conns := parseLines(input, 1935)
	if len(conns) != 3 {
		t.Fatalf("got %d connections, want 3: %+v", len(conns), conns)
	}
	want0 := Connection{LocalIP: "10.0.0.5", LocalPort: 38211, DestIP: "93.184.216.34", DestPort: 1935, PID: "0"}
	if conns[0] != want0 {
		t.Errorf("conns[0] = %+v, want %+v", conns[0], want0)
	}
	ipv6 := conns[2]
	if ipv6.LocalIP != "2001:db8::1" || ipv6.LocalPort != 51234 ||
		ipv6.DestIP != "2001:db8::2" || ipv6.DestPort != 1935 {
		t.Errorf("ipv6 conn = %+v, want {2001:db8::1 51234 2001:db8::2 1935}", ipv6)
	}
}

func TestParseLinesWithPID(t *testing.T) {
	// Real `ss -H -t -n -p` output includes users:(("proc",pid=N,fd=N)).
	input := []byte("0      0      10.0.0.5:38211    93.184.216.34:1935 users:((\"ffmpeg\",pid=12345,fd=6))\n" +
		"0      0      10.0.0.5:38212    93.184.216.34:1935 users:((\"nginx\",pid=99,fd=23))\n")
	conns := parseLines(input, 1935)
	if len(conns) != 2 {
		t.Fatalf("got %d connections, want 2", len(conns))
	}
	if conns[0].PID != "12345" {
		t.Errorf("conns[0].PID = %q, want 12345", conns[0].PID)
	}
	if conns[1].PID != "99" {
		t.Errorf("conns[1].PID = %q, want 99", conns[1].PID)
	}
}

func TestParsePID(t *testing.T) {
	cases := []struct {
		line string
		want string
	}{
		{`users:(("ffmpeg",pid=12345,fd=6))`, "12345"},
		{`users:(("nginx",pid=99,fd=23))`, "99"},
		{"no process info here", "0"},
		{"", "0"},
	}
	for _, tc := range cases {
		if got := parsePID([]byte(tc.line)); got != tc.want {
			t.Errorf("parsePID(%q) = %q, want %q", tc.line, got, tc.want)
		}
	}
}

func TestParseLinesEmpty(t *testing.T) {
	if got := parseLines(nil, 1935); len(got) != 0 {
		t.Errorf("parseLines(nil) = %d conns, want 0", len(got))
	}
	if got := parseLines([]byte("\n\n"), 1935); len(got) != 0 {
		t.Errorf("parseLines(blank lines) = %d conns, want 0", len(got))
	}
}
