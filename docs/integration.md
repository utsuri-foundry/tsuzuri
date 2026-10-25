# Library integration and recovery

The root package is imported as `github.com/utsuri-foundry/tsuzuri` (package
name `tsuzuri`). The CLI uses this same implementation. Pin a reviewed version;
pre-1.0 APIs may change. Native Git is required on the executing host.

## Calls and errors

`Open(path)` selects a local repository. `Show` and `Next` inspect tasks; `Claim`
and `Accept` return typed ownership results. `Query`, `Coordinate` and `Author`
cover the wider CLI surface. Operations accept `context.Context` and optional
`OperationOptions`. Keep task ID, worker ID and repository identity distinct.

```go
client, err := tsuzuri.Open(repositoryPath)
if err != nil {
    return err
}
task, source, err := client.Show(ctx, taskID, policy)
if err != nil {
    var failure *tsuzuri.Error
    if errors.As(err, &failure) && failure.Kind == "validation" {
        // Inspect configuration and input before retrying.
    }
    return err
}
```

`*Error` retains Kind, Code, ExitCode, structured recovery details and inspectable
causes. Do not classify errors by message text.

| Condition | Kind / code | Exit |
| --- | --- | --- |
| Malformed operation options | `usage` | 2 |
| Invalid input or expected-policy mismatch | `validation` | 3 |
| Unavailable task / canonical checkout refusal | `unavailable` / `canonical-checkout` | 4 |
| Claim, ownership, replay or digest conflict | `claim-conflict`, `lost-ownership`, `idempotency-conflict`, `stale-input` | 5 |
| Remote configuration or transport failure | `remote` | 6 |
| Uncertain write or reconciliation outcome | `indeterminate` | 7 |
| Preserved implementation / materialization failure | `preserved-work` / `local-materialization` | 8 |

This is not a retry table. `remote` includes deterministic configuration failures;
uncertain writes require reconciliation, not blind re-execution. Cancellation
causes remain inspectable with `errors.Is`; classification alone is insufficient.

## Trusted policy and effects

A supervisor may supply an absolute Git executable, a complete child environment
and expected repository ID, claim namespace and effective remote URL. A nonempty
Expected field that disagrees with observed configuration returns
`Kind == Code == "validation"`, exit 3. The policy is well formed but the selected
store violates it; correct policy/configuration before retrying.

The supplied environment is copied per operation. The client retains no
credentials and does not change process environment. Nil uses standalone CLI
defaults. Repository-selection and tracing variables are stripped. The caller
owns credential helper, global config, hook, redirect and transport policy.
Supply secrets through the environment/configuration boundary, not command-line
arguments. These checks do not make a concurrently edited clone an adversarial
sandbox.

Policy refusal creates no claim, assignment or receipt. Canonical configuration
checks can first fetch into local bootstrap/snapshot refs. Serialize local file
and worktree authoring on a shared checkout; do not mutate supplied slices during
a call. Use separate clones for independent concurrent operators.

Operations are bounded to two minutes overall and 30 seconds per Git command;
earlier caller deadlines win. Command output is capped at 64 MiB, stderr at
64 KiB and individual records at 1 MiB. History uses bounded first-parent replay,
not a persistent validation cache. Confirm representative history latency under
your own caller deadline.

CLI stdin is synchronously bounded. Inherited streams must support the OS poller;
regular-file reads retain OS I/O limitations. The CLI needs exclusive use of its
stdin stream: duplicated descriptors share blocking flags, and abrupt termination
can prevent restoring the original mode.

## Ownership invariants

- Readiness is dependency information, not permission to execute work.
- Claims use conditional remote ref updates. Original task revision and acceptance
  text stay bound to the claim; later text changes must not refresh authority.
- Assignments identify the current worker and generation. Reverify ownership and
  task drift after accept and before execution or publication.
- Claim and assignment refs are separate. They are not an atomic execution fence
  and cannot stop a process already running under stale observations.
- Published implementation retains claim-anchor ancestry. Unexpected tip advances
  block automatic recovery rather than adopting unverified work.
- A terminal record is complete only on the canonical base. A PR, local commit or
  finished subprocess is not canonical completion. Terminal IDs are permanent.

## Interrupted work

Retain clone-local receipts, exact worker identity, original task revision and
verified checkpoint. Lost responses may conceal successful remote writes: inspect
and reconcile them. When both a response and receipts are lost, recovery is only
for the exact authorized worker and matching original revision. Assignment changes
and pending handoffs must prevent an outgoing worker from acting as owner.

Use `status`, `show --json` and `doctor` to collect evidence. `materialize` resumes
local worktree creation; it does not grant new authority. `accept --no-worktree`
can recover a matching external worker without creating a workspace. Release is
restricted to unused claims and must not discard implementation.

An assignment with no claim is an orphan. A new claim must not overwrite it.
Preserve the observed refs, original receipt/anchor, assignment chain and any
implementation branch/checkpoint. Coordinate all writers before considering
repair. Missing original evidence or an unverified tip requires maintainer
investigation. There is no automatic destructive repair command; do not delete
refs or retry with a weaker client to bypass a refusal.

## Existing stores

### Corrected terminal records

From v0.1.0-rc.5, first-parent replay validates historical terminal frontmatter
and lifecycle facts without requiring a valid `# Resolution` section in every
old `done` or `cancelled` revision. Correct the section in a new canonical-base
commit, preserving the task's ID, path and terminal state. Current records still
require an exact, nonempty `# Resolution`; history is neither rewritten nor
skipped. Historical nonterminal `# Outcome` and `# Acceptance` sections, project
records, configuration, record framing and bounds remain validated. Removed or
reused IDs and terminal removal, reopening, movement or state changes still fail.

This deliberately differs from earlier releases, which reject even corrected
terminal-section mistakes. Older clients can remain blocked on such a store;
switching back to an older executable does not undo its history. No wire format
or coordination ref changes are required. Validate a disposable copy before
upgrading operators.

Original claimed snapshots remain strictly validated, including their terminal
records. A later correction cannot repair an invalid original snapshot or refresh
its acceptance text. Preserve claim/assignment refs and receipts; if a claim's
original snapshot is invalid, stop and involve the repository owner rather than
deleting evidence or bypassing validation. After a correction reaches the base,
use `validate --source base`, `show --json` and `status --json`, then perform the
normal exact-worker recovery and ownership re-verification.

Record parsing and lifecycle failures identify the immutable commit and path in
the message and `Error.Details` (`commit`, `path`); existing error classifications
are preserved. Object-read, framing and aggregate-bound failures may be reported
at operation level when no individual record can be identified.

### Switching executables

Keep schema-1 configuration, Markdown records, Git objects/refs and local receipts.
Do not reinitialize or rewrite history to switch binaries. Validate a disposable
copy first, retain the old executable, and qualify one operator/task before
changing shared PATH or a service profile. Quiesce or reconcile active workers;
rollback changes the executable selection, not the remote coordination history.
