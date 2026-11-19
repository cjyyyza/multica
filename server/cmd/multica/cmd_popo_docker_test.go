package main

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"testing"
)

func TestPopoDockerLocalAPIPortRequiresLocalAddress(t *testing.T) {
	const local = "192.0.2.20"
	const remote = "192.0.2.21"
	localAddrs := func() ([]net.Addr, error) {
		return []net.Addr{&net.IPAddr{IP: net.ParseIP(local)}}, nil
	}
	for _, tc := range []struct {
		name      string
		addresses []string
		want      string
	}{
		{"server address", []string{local}, "18080"},
		{"loopback", []string{"127.0.0.1", "::1"}, "18080"},
		{"remote desktop", []string{remote}, ""},
		{"ambiguous DNS", []string{local, remote}, ""},
		{"unresolved hostname", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lookup := func(context.Context, string, string) ([]netip.Addr, error) {
				var ips []netip.Addr
				for _, address := range tc.addresses {
					ips = append(ips, netip.MustParseAddr(address))
				}
				return ips, nil
			}
			got := popoDockerLocalAPIPort(t.Context(), "http://multica.example.test:18080", lookup, localAddrs)
			if got != tc.want {
				t.Fatalf("API port = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDiscoverDockerPopoTargetMatchesPublishedAPIPort(t *testing.T) {
	other := strings.Repeat("a", 64)
	matching := strings.Repeat("b", 64)
	docker := func(args ...string) ([]byte, error) {
		switch args[0] {
		case "ps":
			return []byte(other + " 0:0\n" + matching + " 1001:1001\n"), nil
		case "inspect":
			if args[len(args)-1] == matching {
				return []byte(`{"8080/tcp":[{"HostPort":"18080"},{"HostPort":"18080"}]}`), nil
			}
			return []byte(`{"8080/tcp":[{"HostPort":"18081"}],"3000/tcp":[{"HostPort":"18080"}]}`), nil
		default:
			t.Fatalf("unexpected Docker command %v", args)
			return nil, nil
		}
	}
	target, err := discoverDockerPopoTarget("18080", docker)
	if err != nil || target.id != matching || target.user != "1001:1001" {
		t.Fatalf("selected %+v, err = %v", target, err)
	}
	if _, err := discoverDockerPopoTarget("18082", docker); err == nil {
		t.Fatal("selected a container without a matching API port")
	}
}

func TestLocalDockerCommandIgnoresRemoteDockerContext(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://remote.example.test:2376")
	t.Setenv("DOCKER_CONTEXT", "remote")
	cmd := localDockerCommand(t.Context(), "test-docker", "ps")
	if cmd.Args[1] != "--host" || cmd.Args[2] != "npipe:////./pipe/docker_engine" {
		t.Fatalf("Docker arguments = %v", cmd.Args)
	}
	for _, value := range cmd.Env {
		if strings.HasPrefix(value, "DOCKER_HOST=") || strings.HasPrefix(value, "DOCKER_CONTEXT=") {
			t.Fatalf("inherited remote Docker setting %q", value)
		}
	}
}
