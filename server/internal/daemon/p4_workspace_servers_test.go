package daemon

import (
	"testing"

	"github.com/multica-ai/multica/server/internal/daemon/p4cache"
)

func TestP4BindingVerifiesAliasesAndKeepsDepotScope(t *testing.T) {
	bin := writeDaemonFakeP4(t)
	for _, tc := range []struct {
		name, port, cluster, info, stream string
		allowed                           bool
	}{
		{"same cluster", "ssl:commit:1666", "cluster", "... serverCluster cluster\n", "//depot/main", true},
		{"different cluster", "ssl:commit:1666", "cluster", "... serverCluster other\n", "//depot/main", false},
		{"TLS downgrade", "commit:1666", "cluster", "... serverCluster cluster\n", "//depot/main", false},
		{"same cluster different depot", "ssl:commit:1666", "cluster", "... serverCluster cluster\n", "//other/main", false},
		{"server ID alias", "ssl:commit:1666", "server:central@tcp:canonical:1666", "... serverID central\n... serverAddress canonical:1666\n", "//depot/main", true},
		{"P4 ServerID tag", "ssl:commit:1666", "server:central@tcp:canonical:1666", "... ServerID central\n... serverAddress canonical:1666\n", "//depot/main", true},
		{"same ID on another installation", "ssl:commit:1666", "server:central@tcp:canonical:1666", "... ServerID central\n... serverAddress other:1666\n", "//depot/main", false},
		{"ID without a canonical address", "ssl:commit:1666", "server:central@tcp:canonical:1666", "... ServerID central\n", "//depot/main", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("P4_FAKE_INFO", tc.info)
			ws := &p4cache.Workspace{Port: "ssl:edge:1666", ServerCluster: tc.cluster, Stream: tc.stream}
			d := &Daemon{p4Cache: &p4cache.Cache{P4Path: bin}}
			_, _, err := d.matchP4Workspace(t.Context(), []P4DepotData{{Port: tc.port, Depot: "//depot/main", User: "alice"}}, ws)
			if (err == nil) != tc.allowed {
				t.Fatalf("binding error=%v allowed=%v", err, tc.allowed)
			}
		})
	}
}

func TestP4BindingVerifiesRegisteredEdge(t *testing.T) {
	t.Setenv("P4_FAKE_INFO", "... serverID central\n... serverAddress commit:1666\n... serverServices commit-server\n")
	t.Setenv("P4_FAKE_SERVER", "... ServerID edge-1\n... Services edge-server\n... Address ssl:edge:1666\n")
	d := &Daemon{p4Cache: &p4cache.Cache{P4Path: writeDaemonFakeP4(t)}}
	ws := &p4cache.Workspace{Port: "ssl:edge:1666", ServerCluster: "server:edge-1", ServerID: "edge-1", Replica: "ssl:commit:1666", Edge: true, Stream: "//depot/main"}
	ref, _, err := d.matchP4Workspace(t.Context(), []P4DepotData{{Port: "ssl:commit:1666", Depot: "//depot/main", User: "alice"}}, ws)
	if err != nil || ref.Port != "ssl:commit:1666" || ws.ServerCluster != "server:central@tcp:commit:1666" {
		t.Fatalf("registered edge was not bound: %+v %+v %v", ws, ref, err)
	}
}
