# Contributing

Use a feature branch or a sibling worktree. Keep the canonical checkout clean,
coordinate overlapping changes, and preserve other operators' work. Developing
this repository does not require running Tsuzuri.

## Build and check

The optional `nix develop` shell pins Go 1.26.10, Git, make and the C compiler
needed for race tests. Keep `.go-version`, `go.mod`, the Nix lock and CI aligned.
`GOTOOLCHAIN=local` prevents implicit compiler downloads.

```sh
make verify       # formatting, architecture, modules, vet, tests, race, build
make cross-build  # Linux/macOS, amd64/arm64
make fuzz         # bounded parser campaigns
make security     # pinned govulncheck; requires vulnerability database access
```

`make verify` checks formatting without rewriting files. Use `gofmt` when editing.
Review `go mod tidy` changes and the selected module graph; do not upgrade
unrelated dependencies. CLI and optional interoperability checks are documented
in [tests](tests/README.md).

## Changes

- Keep changes focused and use the standard library unless a dependency has a
  concrete benefit. Record new dependency scope, license, update owner and tests.
- Keep entry points thin. Declare source ownership/import boundaries in
  `architecture.json`; `make architecture` also checks file budgets and required
  documentation. Co-located `*_test.go` files are tests, not production code.
- Preserve task identities, wire formats, receipt recovery, conditional ref
  updates and canonical completion. Changes to those contracts need explicit
  compatibility evidence and review.
- Bound external operations and output, propagate cancellation, preserve useful
  error causes and redact credentials. Never introduce process-global credential
  changes or background work without an observed stop path.
- Test effects with temporary repositories and local bare remotes. Required tests
  must work without private services or production credentials. Add regressions
  for meaningful failure paths; compilation is not runtime proof.
- Comments should explain public contracts, constraints and non-obvious reasons.
  Avoid restating names or narrating code. Keep recovery invariants and build
  directives; put tutorials and extended explanations in docs/.
- Report tests run, skipped checks and platform limitations. Obtain independent
  review before merge; publishing, installing and changing visibility are
  separate maintainer actions.

## Packaging

From a clean committed tree, run:

```sh
nix develop --command make release-candidate VERSION=0.1.0-rc.2
```

The version must match `Version` in source. Artifacts under `build/releases/`
include four executables, build provenance, license notices and SHA256 checksums.
The command has a ten-minute deadline and refuses dirty or mismatched inputs.
It neither installs nor publishes. Cross-builds are not native installation
proof; signing/notarization and real credential-helper pilots are separate checks.

## Dependencies

Runtime modules are `go-toml/v2` (TOML) and `goldmark` (CommonMark), both MIT
licensed. Neither needs cgo. Git is an external runtime dependency. Nix, the C
compiler, coreutils and tar are development/build tools.
Review compiler and dependency license notices when updating pins.
