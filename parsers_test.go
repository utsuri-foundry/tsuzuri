package tsuzuri

import (
	"strings"
	"testing"
)

// Successful parsing must preserve revision identity and authorization fields;
// arbitrary input may be rejected, but must not panic or invent an owner.
func FuzzCoordinationMetadata(f *testing.F) {
	f.Add("")
	f.Add("tsuzuri claim task\n\nTsuzuri-Claim-Version: 1\nTsuzuri-Repository: 0198d2de-18c7-7ca0-a327-e6b4760f8613\nTsuzuri-Task: task\nTsuzuri-Claim: 12345678-1234-4234-8234-123456789abc\nTsuzuri-Owner: worker\nTsuzuri-Base: " + strings.Repeat("a", 40) + "\nTsuzuri-Claimed-At: 2026-10-24T00:00:00Z\n")
	f.Add("tsuzuri assignment task\n\nTsuzuri-Assignment-Version: 1\nTsuzuri-Repository: 0198d2de-18c7-7ca0-a327-e6b4760f8613\nTsuzuri-Task: task\nTsuzuri-Claim: 12345678-1234-4234-8234-123456789abc\nTsuzuri-Assignment: 12345678-1234-4234-8234-123456789abc\nTsuzuri-State: assigned\nTsuzuri-Worker: worker\nTsuzuri-Previous: " + strings.Repeat("b", 40) + "\nTsuzuri-Updated-At: 2026-10-24T00:00:00Z\n")
	f.Fuzz(func(t *testing.T, body string) {
		if len(body) > maxRecord {
			t.Skip()
		}
		anchor := strings.Repeat("c", 40)
		if m, err := parseClaim(body, anchor); err == nil {
			if m.AnchorObjectID != anchor || !validIdentity(m.Owner) || !validObject(m.BaseObjectID) {
				t.Fatal("invalid claim accepted")
			}
		}
		if m, err := parseAssignment(body, anchor); err == nil {
			if m.ObjectID != anchor || !validObject(m.PreviousObjectID) {
				t.Fatal("invalid assignment accepted")
			}
			if m.State == "assigned" && (m.Worker == nil || !validIdentity(*m.Worker)) {
				t.Fatal("assignment without valid worker")
			}
		}
	})
}
func FuzzTaskDocument(f *testing.F) {
	f.Add("")
	f.Add(historyTerminal())
	f.Add(strings.ReplaceAll(historyTerminal(), "# Resolution", "# Delivery"))
	f.Add("+++\nschema = 1\nid = \"task\"\nproject = \"project\"\ntitle = \"Task\"\nkind = \"delivery\"\nstate = \"ready\"\npriority = \"p1\"\nblocked_by = []\nareas = []\nrisks = []\ncreated = 2026-10-24\n+++\n\n# Outcome\n\nChange\n\n# Acceptance\n\n- Tested\n")
	f.Add("+++\nid = [\n+++\n# Outcome\n")
	f.Fuzz(func(t *testing.T, body string) {
		if len(body) > maxRecord {
			t.Skip()
		}
		_, _ = parseTask(body, "tasks/project/task.task.md", false)
		strict, strictErr := parseTask(body, "", true)
		historical, historyErr := parseHistoricalTask(body)
		if strictErr == nil && (historyErr != nil || strict.ID != historical.ID || strict.State != historical.State) {
			t.Fatal("historical parsing changed valid identity or state")
		}
		if historyErr == nil && !terminal(historical.State) && strictErr != nil {
			t.Fatal("historical parsing relaxed a nonterminal record")
		}
	})
}
