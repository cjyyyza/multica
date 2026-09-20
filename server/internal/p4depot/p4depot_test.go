package p4depot

import (
	"strings"
	"testing"
)

func TestNormalizeAcceptsCommonPortsAndDepots(t *testing.T) {
	t.Parallel()

	good := []Ref{
		{Port: "perforce.example.com:1666", Depot: "//depot/proj"},
		{Port: "ssl:perforce.example.com:1666", Depot: "//depot/proj/..."},
		{Port: "tcp:localhost:1666", Depot: "//depot"},
		{Port: "ssl:[::1]:1666", Depot: "//stream_depot/main"},
		{Port: "perforce", Depot: "//depot/game/src"},
		{Port: "rsh:ssh perforce", Depot: "//depot/a"},
		{
			Port: "  ssl:p4.example.com:1666  ", Depot: "  //depot/x  ",
			Stream: "  //depot/x/main  ", User: "alice", Charset: "utf8",
			Changelist: "12345", Description: "  game  ",
		},
	}
	for _, in := range good {
		got, err := Normalize(in)
		if err != nil {
			t.Errorf("Normalize(%+v) error = %v", in, err)
			continue
		}
		if got.Port == "" || got.Depot == "" {
			t.Errorf("Normalize(%+v) dropped required fields: %+v", in, got)
		}
	}
}

func TestNormalizeRejectsInvalidValues(t *testing.T) {
	t.Parallel()

	bad := []Ref{
		{Depot: "//depot/proj"},
		{Port: "perforce:1666"},
		{Port: "https://github.com/org/repo", Depot: "//depot/x"},
		{Port: "perforce:1666", Depot: "depot/x"},
		{Port: "perforce:1666", Depot: "//"},
		{Port: "ssl: host:1666", Depot: "//depot/x"},
		{Port: "perforce:1666", Depot: "//depot/x", User: "bad user"},
		{Port: "perforce:1666", Depot: "//depot/x", Changelist: "not a cl!"},
		{Port: "perforce:1666", Depot: "//depot/x", Stream: "main"},
	}
	for _, in := range bad {
		if _, err := Normalize(in); err == nil {
			t.Errorf("Normalize(%+v) succeeded, want error", in)
		}
	}
}

func TestIdentityIgnoresUserAndChangelist(t *testing.T) {
	t.Parallel()

	a := Ref{Port: "SSL:P4.EXAMPLE.COM:1666", Depot: "//depot/x", User: "a", Changelist: "1"}
	b := Ref{Port: "ssl:p4.example.com:1666", Depot: "//depot/x", User: "b", Changelist: "2"}
	if Identity(a) != Identity(b) {
		t.Fatalf("Identity() = %q vs %q", Identity(a), Identity(b))
	}
	c := Ref{Port: "ssl:p4.example.com:1666", Depot: "//depot/x", Stream: "//depot/x/main"}
	if Identity(a) == Identity(c) {
		t.Fatal("stream must change identity")
	}
}

func TestClientNameIsStableAndShort(t *testing.T) {
	t.Parallel()

	r := Ref{Port: "p4:1666", Depot: "//depot/x"}
	a := ClientName("ws-1", "task-1", r)
	b := ClientName("ws-1", "task-1", r)
	if a != b {
		t.Fatalf("ClientName not stable: %q vs %q", a, b)
	}
	if a != ClientName("ws-1", "task-1", Ref{Port: "P4:1666", Depot: "//depot/x", User: "other"}) {
		t.Fatal("ClientName should ignore user")
	}
	if len(a) > 32 || !strings.HasPrefix(a, "mc") {
		t.Fatalf("ClientName = %q, want short mc… name", a)
	}
}

func TestSyncPath(t *testing.T) {
	t.Parallel()

	cases := []struct {
		depot, cl, want string
	}{
		{"//depot/x", "", "//depot/x/..."},
		{"//depot/x/...", "", "//depot/x/..."},
		{"//depot/x/file.c", "", "//depot/x/file.c"},
		{"//depot/x", "12", "//depot/x/...@12"},
		{"//depot/x", "#head", "//depot/x/...#head"},
		{"//depot/x", "@rel", "//depot/x/...@rel"},
	}
	for _, tc := range cases {
		if got := SyncPath(tc.depot, tc.cl); got != tc.want {
			t.Errorf("SyncPath(%q, %q) = %q, want %q", tc.depot, tc.cl, got, tc.want)
		}
	}
}

func TestViewMapsPlacesProjectAtCheckoutRoot(t *testing.T) {
	for _, tc := range []struct{ depot, source, target string }{
		{"//depot/UE", "//depot/UE/...", "..."},
		{"//depot/UE/...", "//depot/UE/...", "..."},
		{"//depot/UE/Project.uproject", "//depot/UE/Project.uproject", "Project.uproject"},
	} {
		source, target := ViewMaps(tc.depot)
		if source != tc.source || target != tc.target {
			t.Fatalf("%s mapped to %s %s", tc.depot, source, target)
		}
	}
}
