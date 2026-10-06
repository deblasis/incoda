package lane

import (
	"encoding/json"
	"testing"
)

func TestGateTicketFieldsRoundTrip(t *testing.T) {
	raw := `{"pid":123,"queue":"k","slots":1,"command":["true"],"max_cpu_pct":30,"idle_for_nanos":120000000000}`
	var tk Ticket
	if err := json.Unmarshal([]byte(raw), &tk); err != nil {
		t.Fatal(err)
	}
	if tk.MaxCPUPct != 30 || tk.IdleForNanos != 120000000000 {
		t.Fatalf("gate fields lost: %+v", tk)
	}
	// Old binaries ignore unknown fields: new fields must be omitempty and
	// must not affect effectiveSlots/SlotsDisagree.
	entries := []Entry{{Ticket: Ticket{Slots: 1}}, {Ticket: Ticket{Slots: 1, MaxCPUPct: 30}}}
	if SlotsDisagree(entries) {
		t.Fatal("gate fields must not count as slots disagreement")
	}
	if got := effectiveSlots(entries, 0); got != 1 {
		t.Fatalf("gate fields must not change width: got %d", got)
	}
}
