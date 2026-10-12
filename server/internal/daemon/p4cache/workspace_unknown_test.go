package p4cache

import "testing"

// Server answers captured from the DJ01 commit server. Only the first two
// mean the changelist is gone; the rest are failures that must not be read
// as a deletion.
func TestChangeIsUnknownMatchesOnlyMissingChangelists(t *testing.T) {
	for _, tc := range []struct {
		out  string
		want bool
	}{
		{"Change 999999999 unknown.", true},
		{"999999999 - no such changelist.", true},
		{"Client 'multica-blocked' unknown - use 'client' command to create it.", false},
		{"Perforce client error:\n\tConnect to server failed; check $P4PORT.\n\tTCP connect to 127.0.0.1:1 failed.", false},
		{"Unknown command.  Try 'p4 help' for info.", false},
		{"... change 795115\n... status submitted\n... oldChange 795095", false},
	} {
		if got := changeIsUnknown(tc.out); got != tc.want {
			t.Errorf("changeIsUnknown(%q) = %v, want %v", tc.out, got, tc.want)
		}
	}
}
