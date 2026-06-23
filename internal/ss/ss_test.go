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
		ip, port, ok := splitAddrPort(tc.in)
		if ip != tc.ip || port != tc.port || ok != tc.ok {
			t.Errorf("splitAddrPort(%q) = (%q, %d, %v), want (%q, %d, %v)",
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
	want0 := Connection{LocalIP: "10.0.0.5", LocalPort: 38211, DestIP: "93.184.216.34", DestPort: 1935}
	if conns[0] != want0 {
		t.Errorf("conns[0] = %+v, want %+v", conns[0], want0)
	}
	ipv6 := conns[2]
	if ipv6.LocalIP != "2001:db8::1" || ipv6.LocalPort != 51234 ||
		ipv6.DestIP != "2001:db8::2" || ipv6.DestPort != 1935 {
		t.Errorf("ipv6 conn = %+v, want {2001:db8::1 51234 2001:db8::2 1935}", ipv6)
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
