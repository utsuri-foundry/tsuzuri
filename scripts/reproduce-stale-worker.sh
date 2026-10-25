#!/bin/sh
# Local-only reproduction. Pass an absolute path to Rust v0.3.1 or the Go candidate.
set -eu
binary=${1:?usage: reproduce-stale-worker.sh /absolute/path/to/tsuzuri}
case "$binary" in /*) ;; *) printf '%s\n' 'binary must be an absolute path' >&2; exit 2 ;; esac
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT HUP INT TERM
export HOME="$scratch/home" GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null
export GIT_CONFIG_NOSYSTEM=1 GIT_TERMINAL_PROMPT=0 GIT_ALLOW_PROTOCOL=file
unset TSUZURI_WORKER GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_CONFIG_COUNT GIT_CONFIG_PARAMETERS
mkdir -p "$HOME"
git init --quiet --bare --initial-branch=main "$scratch/remote.git"
git clone --quiet "$scratch/remote.git" "$scratch/a"
cd "$scratch/a"
git config user.name Test
git config user.email test@invalid
git config commit.gpgSign false
git config tsuzuri.remote origin
git config tsuzuri.owner test-owner
cat > .tsuzuri.toml <<'CONFIG'
schema = 1
repository_id = "0198d2de-18c7-7ca0-a327-e6b4760f8613"
task_root = "tasks/projects"
base_branch = "main"
claim_namespace = "tsuzuri/tasks"
worktree_root = ".."
worktree_template = "wt-{task_id}"
areas = []
risks = []
CONFIG
mkdir -p tasks/projects/example/tasks
cat > tasks/projects/example/project.md <<'PROJECT'
+++
schema = 1
id = "example"
title = "Example"
state = "active"
+++

# Goal

Coordinate work.

# Finish line

Tests pass.
PROJECT
cat > tasks/projects/example/tasks/first-task.task.md <<'TASK'
+++
schema = 1
id = "first-task"
project = "example"
title = "First task"
kind = "delivery"
state = "ready"
priority = "p1"
blocked_by = []
areas = []
risks = []
created = 2026-10-24
+++

# Outcome

A useful change.

# Acceptance

Tests pass.
TASK
git add .
git commit --quiet -m fixture
git push --quiet origin main
"$binary" claim first-task --no-worktree --worker worker-a >/dev/null
anchor=$(git rev-parse refs/tsuzuri/receipts/0198d2de-18c7-7ca0-a327-e6b4760f8613/first-task)
git clone --quiet "$scratch/remote.git" "$scratch/b"
cd "$scratch/b"
git config tsuzuri.remote origin
git config tsuzuri.owner test-owner
"$binary" accept first-task --no-worktree --worker worker-a >/dev/null
"$binary" handoff first-task --checkpoint "$anchor" --to worker-b >/dev/null
cd "$scratch/a"
set +e
"$binary" --json claim first-task
result=$?
set -e
printf '\nStale retry exit: %s\n' "$result"
if test -d "$scratch/wt-first-task"; then
    printf '%s\n' 'STALE WORKTREE CREATED (observed Rust v0.3.1 behavior)'
else
    printf '%s\n' 'No stale worktree created (required Go behavior)'
fi
printf '%s\n' 'Current remote assignment:'
git --git-dir="$scratch/remote.git" show -s --format=%B refs/heads/tsuzuri/tasks-assignments/first-task
