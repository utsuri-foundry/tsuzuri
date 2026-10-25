# Tsuzuri

Git-native, forge-independent task coordination for humans and coding agents
working across machines and worktrees.

Tasks are readable Markdown files. Ownership is established through conditional
writes to remote Git refs, so independent workers can coordinate through a shared
repository. Uncertain writes are reconciled; recovery checks the original task
revision and the worker's current assignment.

Use the `tsuzuri` CLI or embed the Go library. No coordination service or forge
API is required. [Try it locally](docs/quickstart.md) with a disposable bare remote.

## What it owns

- Project and task records, dependencies, acceptance criteria and validation.
- Ready-work discovery, ordering and a shared overview.
- Remote claims, worker assignments and direct or open handoffs.
- Sibling worktrees, interrupted setup recovery and unused-claim release.
- Completion recorded on the canonical base branch.

Your tools own execution authority, agent processes, review, merging and deployment.
Claims and assignments are ownership observations; they cannot stop an already
running worker. A pull request alone does not complete a task.

## Core workflow

From a [configured repository](docs/usage.md):

```sh
tsuzuri validate
tsuzuri overview
tsuzuri next
tsuzuri show improve-search
tsuzuri claim improve-search --worker developer-laptop
tsuzuri status improve-search
```

`claim` establishes remote ownership before creating an implementation branch
and sibling worktree. Work there, then integrate the changes and terminal task
record into the configured base branch.

Use `materialize` to resume interrupted worktree setup, `handoff` and `accept`
to transfer assignments, and `release` to relinquish an unused claim. Release
preserves implementation commits and local changes.

For automation, use `--json` and discover supported operations with
`tsuzuri compatibility --json`. Coordinators managing their own workspaces can
claim and accept with `--no-worktree`. See [integration and recovery](docs/integration.md).

## Install

Download a matching binary and its checksums from [Releases](https://github.com/utsuri-foundry/tsuzuri/releases).
The executable is `tsuzuri`. Supported targets are Linux and macOS on amd64 and
arm64, with Git 2.40+. Binaries do not require Go or Nix. Windows is unsupported;
prerelease binaries are unsigned and not notarized.

To build from source, use Go 1.26.10 or the optional `nix develop` shell:

```sh
git clone https://github.com/utsuri-foundry/tsuzuri.git
cd tsuzuri
make build
./bin/tsuzuri --version
```

## Library

Import `github.com/utsuri-foundry/tsuzuri` (package `tsuzuri`):

```go
client, err := tsuzuri.Open(repositoryPath)
if err != nil {
    return err
}
task, source, err := client.Show(ctx, taskID)
```

The CLI and library share one implementation. Calls accept a context and optional
trusted policy; see [library integration](docs/integration.md) for ownership checks
and error handling. The API is pre-1.0; pin a reviewed version.

## Documentation

- [Local quickstart](docs/quickstart.md)
- [Repository setup and CLI](docs/usage.md)
- [Library integration and recovery](docs/integration.md)
- [Source organization](docs/architecture/README.md)
- [Contributing and checks](CONTRIBUTING.md)

## License

[Apache-2.0](LICENSE). See [NOTICE](NOTICE) for attribution and third-party terms.
