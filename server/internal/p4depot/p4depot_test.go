package p4depot

import (
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
			SwarmURL: " https://swarm.example.com/ ",
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
		{Port: "perforce:1666", Depot: "//depot/x", SwarmURL: "ftp://swarm.example.com"},
		{Port: "perforce:1666", Depot: "//depot/x", SwarmURL: "https://user:ticket@swarm.example.com"},
	}
	for _, in := range bad {
		if _, err := Normalize(in); err == nil {
			t.Errorf("Normalize(%+v) succeeded, want error", in)
		}
	}
}

func TestNormalizeTrimsSwarmURL(t *testing.T) {
	t.Parallel()
	got, err := Normalize(Ref{
		Port: "p4:1666", Depot: "//depot/x",
		SwarmURL: " https://swarm.example.com/reviews/ ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.SwarmURL != "https://swarm.example.com/reviews" {
		t.Fatalf("SwarmURL = %q", got.SwarmURL)
	}
}

func TestIdentityIgnoresUserAndChangelist(t *testing.T) {
	t.Parallel()

	a := Ref{Port: "SSL:P4.EXAMPLE.COM:1666", Depot: "//depot/x", User: "a", Changelist: "1"}
	b := Ref{Port: "ssl:p4.example.com:1666", Depot: "//depot/x", User: "b", Changelist: "2", SwarmURL: "https://swarm.example.com"}
	if Identity(a) != Identity(b) {
		t.Fatalf("Identity() = %q vs %q", Identity(a), Identity(b))
	}
	c := Ref{Port: "ssl:p4.example.com:1666", Depot: "//depot/x", Stream: "//depot/x/main"}
	if Identity(a) == Identity(c) {
		t.Fatal("stream must change identity")
	}
}
