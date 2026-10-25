# CLI guide

Tsuzuri uses a Git repository as a shared task store. Task facts live on a
canonical branch; remote claim and assignment refs coordinate independent clones.
Each operator keeps local receipts and implementation worktrees.

## Set up a repository

Start with a Git repository and a writable remote. Add `.tsuzuri.toml` on its
canonical branch. Choose a new UUID for `repository_id`; it must be unique to
this task store and remain stable across clones.

```toml
schema = 1
repository_id = "0198d2de-18c7-7ca0-a327-e6b4760f8613" # replace for your store
base_branch = "main"
task_root = "tasks/projects"
claim_namespace = "tsuzuri/tasks"
worktree_root = ".."
worktree_template = "wt-{task_id}"
areas = ["core"]
risks = []
```

Configure each clone with its remote and a stable owner identity:

```sh
git config --local tsuzuri.remote origin
git config --local tsuzuri.owner developer-laptop
```

Create `tasks/projects/search/project.md`:

```markdown
+++
schema = 1
id = "search"
title = "Search"
state = "active"
+++

# Goal

Make search useful and predictable.

# Finish line

Queries return relevant, tested results.
```

Create `tasks/projects/search/tasks/improve-search.task.md`:

```markdown
+++
schema = 1
id = "improve-search"
project = "search"
title = "Handle empty search queries"
kind = "defect"
state = "ready"
priority = "p2"
blocked_by = []
areas = ["core"]
risks = []
created = 2026-10-25
+++

# Outcome

Empty queries show a useful prompt.

# Acceptance

An empty or whitespace-only query does not issue a search request.
```

Validate the local records with `tsuzuri validate --source worktree`, then commit
and integrate the configuration and records into the remote canonical branch.
Keep records UTF-8 with LF line endings. IDs are lowercase hyphenated identifiers;
terminal IDs cannot be removed, reused or reopened. Dependencies use task IDs in
`blocked_by`. Referenced areas and risks must be declared in configuration.

There is no `init` command. Existing stores keep their repository ID, paths and
coordination state; do not reinitialize them to change executables.

## Work on a task

```sh
tsuzuri overview
tsuzuri next
tsuzuri show improve-search
tsuzuri claim improve-search --worker developer-laptop
```

`overview` lists unfinished work and claim issues. Completed/cancelled tasks and
retained terminal claims appear as summary counts; `overview --json` includes
their full details. Use `list --canonical-state done` to inspect completed tasks.
Terminal colors are automatic; `NO_COLOR`, `TERM=dumb` or piped output disables
them. Override with `--color always` or `--color never`.

`stale-terminal` means a claim is retained for a task that is already `done` or
`cancelled` on the canonical base; the overview calls these retained terminal
claims. They are historical coordination evidence, not
active ownership or incomplete work. A malformed claim or assignment remains a
separate claim issue, even for a completed task.

Continue in the branch and sibling worktree printed by `claim`. Commit the
implementation and mark the task `done` with its resolution in the same change.
Use your normal review/integration process. Completion is recognized only when
the terminal task record reaches the configured canonical base.

`status <id>` inspects ownership and recovery state. `materialize <id>` resumes
interrupted worktree creation. A process exiting does not abandon a claim.

To transfer work, publish a verified checkpoint on the implementation branch,
then use `handoff <id> --checkpoint <commit> --to <worker>`. The incoming worker
runs `accept <id> --worker <worker>`. Omitting `--to` leaves an open handoff;
acceptance still requires an explicit incoming worker identity.

`release <id>` is only for an unused claim. It cannot discard implementation
work. Keep receipts, claim anchors and assignment history during recovery.
See [ownership and recovery](integration.md) before repairing inconsistent state.

## Automation

`--json` emits a schema-1 envelope on stdout. Start with
`tsuzuri compatibility --json` and check required capabilities. Do not parse
human-readable output. Query defaults may fetch remote state and create local
snapshot refs; inspection is not necessarily an offline operation.

| Purpose | Commands |
| --- | --- |
| Validate and discover | `validate`, `overview`, `projects`, `project`, `list`, `next` |
| Inspect | `show`, `status`, `doctor` |
| Own and recover work | `claim`, `materialize`, `handoff`, `accept`, `release` |
| Author records | `create-project`, `create-task`, `transition-task` |
| Discover the interface | `compatibility`, `--help`, `--version` |

Authoring commands accept JSON through `--input <file>` (or stdin when omitted)
and require `--request-id <stable-id>` for replay. Retain the request ID and exact
body when recovering an uncertain response. Input shapes are the library's
`CreateProjectInput`, `CreateTaskInput` and `TransitionTaskInput` types. Authoring
requires a named non-canonical worktree branch. It changes local files; it does
not publish or merge them.

External workspace managers can use `claim --no-worktree --worker <identity>
--expected-digest <digest>` and `accept --no-worktree --worker <identity>`. Retain
`claimedTask`, the original Markdown and assignment identity; a refreshed task
view must not silently replace accepted requirements.
