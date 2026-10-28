package p4depot

import "testing"

func TestEndpointEquivalenceKeepsTransport(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		same bool
	}{
		{"P4:1666", "tcp:p4:1666", true}, {"tcp4:p4:1666", "p4:1666", true},
		{"ssl4:p4:1666", "ssl:p4:1666", true}, {"ssl:p4:1666", "tcp:p4:1666", false},
		{"tcp6:p4:1666", "tcp4:p4:1666", false}, {"ssl6:p4:1666", "ssl:p4:1666", false},
		{"tcp46:p4:1666", "p4:1666", false}, {"ssl64:p4:1666", "ssl:p4:1666", false},
	} {
		if got := SameEndpoint(tc.a, tc.b); got != tc.same {
			t.Errorf("%q and %q same=%v", tc.a, tc.b, got)
		}
	}
	if SameTransport("ssl:p4:1666", "p4:1666") || SameTransport("rsh:program", "p4:1666") {
		t.Fatal("unsafe endpoint transport accepted")
	}
}
