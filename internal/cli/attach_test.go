package cli

import (
	"strings"
	"testing"

	"github.com/deblasis/incoda/internal/lane"
)

// TestMayAttachGate: attach is limited to the job's own owner, and every way
// that gate can fail names who won and who asked. The refusal is a usage
// error (120), same as every other named refusal.
func TestMayAttachGate(t *testing.T) {
	job := lane.Ticket{PID: 4242, Queue: "builds", Owner: "alice"}
	cases := []struct {
		name, me string
		ticket   lane.Ticket
		wantOK   bool
		wantMsg  []string
	}{
		{
			name:   "owner matches",
			me:     "alice",
			wantOK: true,
		},
		{
			name:   "owner matches after whitespace",
			me:     "  alice  ",
			wantOK: true,
		},
		{
			name:    "no identity",
			me:      "",
			wantMsg: []string{"--owner or $INCODA_OWNER"},
		},
		{
			name:    "identity is only spaces",
			me:      "   ",
			wantMsg: []string{"--owner or $INCODA_OWNER"},
		},
		{
			name:    "another owner",
			me:      "bob",
			wantMsg: []string{"belongs to owner \"alice\"", "you are \"bob\"", "Only the job's owner may attach"},
		},
		{
			name:    "ticket without owner",
			me:      "bob",
			ticket:  lane.Ticket{PID: 4242, Queue: "builds"},
			wantMsg: []string{"recorded no owner", "refused"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tk := tc.ticket
			if tk.Owner == "" && tc.ticket.PID == 0 {
				tk = job
			}
			err := mayAttach(tk, tc.me)
			if tc.wantOK {
				if err != nil {
					t.Fatalf("owner %q should attach, got %v", tc.me, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("identity %q should be refused", tc.me)
			}
			for _, want := range tc.wantMsg {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("refusal %q missing %q", err.Error(), want)
				}
			}
		})
	}
}
