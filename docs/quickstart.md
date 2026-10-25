# Try a local task store

This example creates a bare Git remote, an operator clone, one task and its
implementation worktree in a new temporary directory. It needs no account,
network access or production credentials and does not change your Git settings.

From this repository, after `make build`:

```sh
sh examples/quickstart.sh "$PWD/bin/tsuzuri"
```

The script prints the demo directory and runs `validate`, `overview`, `next`,
`claim` and `status`. The task `improve-search` starts ready and becomes claimed
by `developer-laptop`. Its new sibling worktree is where implementation belongs.
Inspect `main/.tsuzuri.toml` and the Markdown under `main/tasks/` to see the store.

A second operator clone using that local remote will observe the same claim.
No pull request or merge is needed to acquire it: the claim is a conditional
update to a remote ref. If the result of a write is uncertain, retain the state
and reconcile ownership rather than assuming the write failed.

Task completion is separate. Implement the change in the printed worktree,
record `state = "done"` and a `# Resolution` section in the task, and integrate
both into the canonical `main` branch using the normal review process. A PR
alone does not complete it. `release` is only for unused claims.

The demo is retained for inspection. Remove only its printed temporary directory
when finished; none of its data is needed by this source repository. Use a new
repository UUID and your normal identities when [configuring a real store](usage.md).
There is no `init` command yet; this example is not an installation or migration
script.
