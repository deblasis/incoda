package lane

import (
	"strings"
	"testing"
)

func TestMayInteractGate(t *testing.T) {
	job := Ticket{PID: 4242, Queue: "builds", Owner: "alice"}
	cases := []struct {
		name    string
		me      string
		ticket  Ticket
		wantOK  bool
		wantMsg []string
	}{
		{name: "the owner", me: "alice", wantOK: true},
		{
			name:   "ticket owner has stray whitespace",
			me:     "alice",
			ticket: Ticket{PID: 4242, Queue: "builds", Owner: " alice "},
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
			wantMsg: []string{"belongs to owner \"alice\"", "you are \"bob\"", "Only the job's owner may type at it"},
		},
		{
			name:    "ticket without owner",
			me:      "bob",
			ticket:  Ticket{PID: 4242, Queue: "builds"},
			wantMsg: []string{"recorded no owner", "refused"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tk := tc.ticket
			if tk.Owner == "" && tc.ticket.PID == 0 {
				tk = job
			}
			err := MayInteract(tk, tc.me)
			if tc.wantOK {
				if err != nil {
					t.Fatalf("owner %q should pass, got %v", tc.me, err)
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
