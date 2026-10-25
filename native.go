package tsuzuri

import "context"

// TaskID identifies a permanent task record within a repository.
type TaskID string

// WorkerID identifies a worker across claim, handoff and recovery operations.
type WorkerID string

// ClaimRequest selects an original task revision and an explicit worker.
// NoWorktree leaves implementation workspace management to the caller.
type ClaimRequest struct {
	Task                            TaskID
	Worker                          WorkerID
	Owner, ExpectedDigest, Worktree string
	NoWorktree                      bool
}

// AcceptRequest selects the incoming or recovering worker and workspace mode.
type AcceptRequest struct {
	Task       TaskID
	Worker     WorkerID
	Worktree   string
	NoWorktree bool
}

// CoordinationSource identifies observed refs using the schema-1 wire format.
type CoordinationSource struct {
	RepositoryID                string   `json:"repositoryId"`
	BaseObjectID                string   `json:"baseObjectId"`
	ConfigurationObjectID       *string  `json:"configurationObjectId"`
	SnapshotID                  string   `json:"snapshotId"`
	ObservedClaimObjectIDs      []string `json:"observedClaimObjectIds"`
	ObservedAssignmentObjectIDs []string `json:"observedAssignmentObjectIds"`
}

// AcceptanceResult records the reconciled assignment and local receipt effects.
type AcceptanceResult struct {
	CoordinationSource
	TaskID                        string              `json:"taskId"`
	ClaimID                       string              `json:"claimId"`
	Assignment                    *AssignmentMetadata `json:"assignment"`
	RemoteRef                     string              `json:"remoteRef"`
	CreatedLocalReceipt           bool                `json:"createdLocalReceipt"`
	UpdatedLocalAssignmentReceipt bool                `json:"updatedLocalAssignmentReceipt"`
	ClaimedTask                   *TaskRevision       `json:"claimedTask"`
	ClaimObjectID                 string              `json:"claimObjectId"`
	Materialization               *Materialization    `json:"materialization"`
}

// HandoffResult identifies the published assignment and removed local receipts.
type HandoffResult struct {
	CoordinationSource
	TaskID                        string              `json:"taskId"`
	ClaimID                       string              `json:"claimId"`
	Assignment                    *AssignmentMetadata `json:"assignment"`
	RemoteRef                     string              `json:"remoteRef"`
	RemovedLocalReceipt           bool                `json:"removedLocalReceipt"`
	RemovedLocalAssignmentReceipt bool                `json:"removedLocalAssignmentReceipt"`
}

// ReleaseResult records conditional release of an unused claim and local cleanup.
type ReleaseResult struct {
	TaskID                   string  `json:"taskId"`
	RemoteRef                string  `json:"remoteRef"`
	AnchorObjectID           string  `json:"anchorObjectId"`
	RemovedWorktree          *string `json:"removedWorktree"`
	RemovedLocalBranch       bool    `json:"removedLocalBranch"`
	RemovedReceipt           bool    `json:"removedReceipt"`
	RemovedAssignmentReceipt bool    `json:"removedAssignmentReceipt"`
	RemoteAlreadyAbsent      bool    `json:"remoteAlreadyAbsent"`
	RemovedRemoteAssignment  bool    `json:"removedRemoteAssignment"`
}

// Next discovers ready work on a fresh canonical snapshot. Readiness grants no
// execution permission. An empty project selects every project.
func (c *Client) Next(ctx context.Context, project string, options ...OperationOptions) ([]TaskView, Source, error) {
	result, err := c.Query(ctx, Query{Command: "next", Project: project}, options...)
	if err != nil {
		return nil, Source{}, err
	}
	return result.Data.([]TaskView), *result.Source, nil
}

// Claim uses the same engine as the standalone CLI, including receipt recovery.
func (c *Client) Claim(ctx context.Context, request ClaimRequest, options ...OperationOptions) (ClaimResult, Source, error) {
	result, err := c.Coordinate(ctx, Coordination{Command: "claim", ID: string(request.Task), Worker: string(request.Worker), Owner: request.Owner, ExpectedDigest: request.ExpectedDigest, Worktree: request.Worktree, NoWorktree: request.NoWorktree}, options...)
	if err != nil {
		return ClaimResult{}, Source{}, err
	}
	return result.Data.(ClaimResult), *result.Source, nil
}

// Accept reconciles the current assignment and recovers clone-local receipts.
// It is not an execution fence: callers must reverify ownership after return.
func (c *Client) Accept(ctx context.Context, request AcceptRequest, options ...OperationOptions) (AcceptanceResult, Source, error) {
	result, err := c.Coordinate(ctx, Coordination{Command: "accept", ID: string(request.Task), Worker: string(request.Worker), Worktree: request.Worktree, NoWorktree: request.NoWorktree}, options...)
	if err != nil {
		return AcceptanceResult{}, Source{}, err
	}
	return result.Data.(AcceptanceResult), *result.Source, nil
}
