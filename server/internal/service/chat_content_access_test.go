package service

import "testing"

func TestAssembleChatContentReaders(t *testing.T) {
	cases := []struct {
		name         string
		creator      string
		requirement  bool
		participants []string
		failed       bool
		want         []string
	}{
		{name: "ordinary creator", creator: "creator", want: []string{"creator"}},
		{name: "ordinary without creator", want: nil},
		{
			name: "requirement participants include creator", creator: "creator", requirement: true,
			participants: []string{"viewer", "creator", "approver", "viewer"},
			want:         []string{"creator", "approver", "viewer"},
		},
		{
			name: "requirement without creator still lists members", requirement: true,
			participants: []string{"approver"},
			want:         []string{"approver"},
		},
		{
			name: "participant lookup failure keeps only creator", creator: "creator", requirement: true, failed: true,
			participants: []string{"approver"},
			want:         []string{"creator"},
		},
		{
			name: "participant lookup failure without creator discards", requirement: true, failed: true,
			participants: []string{"approver"},
			want:         nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := assembleChatContentReaders(tc.creator, tc.requirement, tc.participants, tc.failed)
			if len(got) != len(tc.want) {
				t.Fatalf("readers = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("readers = %v, want %v", got, tc.want)
				}
			}
		})
	}
}
