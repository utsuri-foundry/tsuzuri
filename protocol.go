package tsuzuri

import (
	"regexp"
	"strings"
	"time"
	"unicode"
)

// ClaimMetadata binds a claim to its repository, task, owner and original base.
type ClaimMetadata struct {
	Version        int    `json:"version"`
	RepositoryID   string `json:"repositoryId"`
	TaskID         string `json:"taskId"`
	ClaimID        string `json:"claimId"`
	Owner          string `json:"owner"`
	BaseObjectID   string `json:"baseObjectId"`
	ClaimedAt      string `json:"claimedAt"`
	AnchorObjectID string `json:"anchorObjectId"`
}

// AssignmentMetadata identifies a worker generation and its previous assignment.
type AssignmentMetadata struct {
	Version          int     `json:"version"`
	RepositoryID     string  `json:"repositoryId"`
	TaskID           string  `json:"taskId"`
	ClaimID          string  `json:"claimId"`
	AssignmentID     string  `json:"assignmentId"`
	State            string  `json:"state"`
	Worker           *string `json:"worker"`
	PreviousObjectID string  `json:"previousObjectId"`
	UpdatedAt        string  `json:"updatedAt"`
	ObjectID         string  `json:"objectId"`
}

// TaskRevision identifies the exact task blob at the claim's original base.
type TaskRevision struct {
	BaseObjectID string `json:"baseObjectId"`
	Path         string `json:"path"`
	RecordDigest string `json:"recordDigest"`
}

// ClaimObservation is validated evidence about a claim, not execution authority.
type ClaimObservation struct {
	TaskID      string                 `json:"taskId"`
	RefName     string                 `json:"refName"`
	ObjectID    string                 `json:"objectId"`
	Status      string                 `json:"status"`
	Valid       bool                   `json:"valid"`
	Metadata    *ClaimMetadata         `json:"metadata"`
	ClaimedTask *TaskRevision          `json:"claimedTask"`
	Assignment  *AssignmentObservation `json:"assignment"`
	Drift       []string               `json:"drift"`
	Error       *string                `json:"error"`
}

type AssignmentObservation struct {
	TaskID   string              `json:"taskId"`
	RefName  string              `json:"refName"`
	ObjectID string              `json:"objectId"`
	Status   string              `json:"status"`
	Valid    bool                `json:"valid"`
	Metadata *AssignmentMetadata `json:"metadata"`
	Error    *string             `json:"error"`
}

func (c Config) claimRef(id string) string { return "refs/heads/" + c.ClaimNamespace + "/" + id }
func (c Config) assignmentRef(id string) string {
	return "refs/heads/" + c.ClaimNamespace + "-assignments/" + id
}
func (c Config) receipt(id string) string {
	return "refs/tsuzuri/receipts/" + c.RepositoryID + "/" + id
}
func (c Config) assignmentReceipt(id string) string {
	return "refs/tsuzuri/assignment-receipts/" + c.RepositoryID + "/" + id
}
func validIdentity(s string) bool {
	return identityPattern.MatchString(s)
}

var identityPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@/-]{0,127}$`)

func canonicalTime(s string) bool {
	v, e := time.Parse(time.RFC3339, s)
	return e == nil && v.UTC().Format("2006-01-02T15:04:05Z") == s
}
func uuid4(s string) bool {
	return uuidPattern.MatchString(s) && s[14] == '4' && strings.ContainsRune("89ab", rune(s[19]))
}
func messageLines(s string) ([]string, error) {
	s = strings.TrimSuffix(s, "\n")
	for _, c := range s {
		if c != '\n' && unicode.IsControl(c) {
			return nil, failure("claim-conflict", "record contains forbidden control characters")
		}
	}
	return strings.Split(s, "\n"), nil
}
func trailer(line, prefix string) (string, error) {
	v, ok := strings.CutPrefix(line, prefix)
	if !ok || v == "" || strings.TrimSpace(v) != v {
		return "", failure("claim-conflict", "invalid coordination trailer")
	}
	return v, nil
}
func parseClaim(s, anchor string) (ClaimMetadata, error) {
	m := ClaimMetadata{Version: 1, AnchorObjectID: anchor}
	lines, e := messageLines(s)
	if e != nil {
		return m, e
	}
	if len(lines) != 9 || lines[1] != "" || lines[2] != "Tsuzuri-Claim-Version: 1" {
		return m, failure("claim-conflict", "claim must have canonical nine-line form")
	}
	prefixes := []string{"Tsuzuri-Repository: ", "Tsuzuri-Task: ", "Tsuzuri-Claim: ", "Tsuzuri-Owner: ", "Tsuzuri-Base: ", "Tsuzuri-Claimed-At: "}
	values := make([]string, 6)
	for i, p := range prefixes {
		values[i], e = trailer(lines[i+3], p)
		if e != nil {
			return m, e
		}
	}
	m.RepositoryID, m.TaskID, m.ClaimID, m.Owner, m.BaseObjectID, m.ClaimedAt = values[0], values[1], values[2], values[3], values[4], values[5]
	if lines[0] != "tsuzuri claim "+m.TaskID || !uuidPattern.MatchString(m.RepositoryID) || !uuid4(m.ClaimID) || !validIdentity(m.Owner) || !validObject(m.BaseObjectID) || !canonicalTime(m.ClaimedAt) {
		return m, failure("claim-conflict", "invalid claim metadata")
	}
	return m, nil
}
func parseAssignment(s, object string) (AssignmentMetadata, error) {
	a := AssignmentMetadata{Version: 1, ObjectID: object}
	lines, e := messageLines(s)
	if e != nil {
		return a, e
	}
	if (len(lines) != 10 && len(lines) != 11) || lines[1] != "" || lines[2] != "Tsuzuri-Assignment-Version: 1" {
		return a, failure("claim-conflict", "assignment must have canonical form")
	}
	prefixes := []string{"Tsuzuri-Repository: ", "Tsuzuri-Task: ", "Tsuzuri-Claim: ", "Tsuzuri-Assignment: ", "Tsuzuri-State: "}
	v := make([]string, 5)
	for i, p := range prefixes {
		v[i], e = trailer(lines[i+3], p)
		if e != nil {
			return a, e
		}
	}
	a.RepositoryID, a.TaskID, a.ClaimID, a.AssignmentID, a.State = v[0], v[1], v[2], v[3], v[4]
	next := 8
	if a.State == "assigned" && len(lines) == 11 {
		worker, e := trailer(lines[8], "Tsuzuri-Worker: ")
		if e != nil || !validIdentity(worker) {
			return a, failure("claim-conflict", "invalid worker")
		}
		a.Worker = &worker
		next = 9
	} else if a.State != "handoff-pending" || len(lines) != 10 {
		return a, failure("claim-conflict", "invalid assignment state or worker")
	}
	a.PreviousObjectID, e = trailer(lines[next], "Tsuzuri-Previous: ")
	if e != nil {
		return a, e
	}
	a.UpdatedAt, e = trailer(lines[next+1], "Tsuzuri-Updated-At: ")
	if e != nil {
		return a, e
	}
	if lines[0] != "tsuzuri assignment "+a.TaskID || !uuidPattern.MatchString(a.RepositoryID) || !uuid4(a.ClaimID) || !uuid4(a.AssignmentID) || !validObject(a.PreviousObjectID) || !canonicalTime(a.UpdatedAt) {
		return a, failure("claim-conflict", "invalid assignment metadata")
	}
	return a, nil
}

var objectPattern = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func validObject(s string) bool  { return objectPattern.MatchString(s) }
func stringPtr(s string) *string { return &s }
