#!/bin/sh
# A disposable local demo. The printed directory is retained for inspection.
set -eu
if test "$#" -ne 1; then
    printf 'Usage: sh examples/quickstart.sh /absolute/path/to/tsuzuri\n' >&2
    exit 2
fi
case "$1" in /*) binary=$1 ;; *) printf 'Use an absolute CLI path.\n' >&2; exit 2 ;; esac
test -x "$binary"
# Keep the demo outside build shells' auto-cleaned TMPDIR directories.
demo=$(mktemp -d /tmp/tsuzuri-demo.XXXXXX)
printf 'Demo directory: %s\n' "$demo"
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null
export GIT_CONFIG_NOSYSTEM=1 GIT_ALLOW_PROTOCOL=file GIT_TERMINAL_PROMPT=0
unset GIT_DIR GIT_WORK_TREE GIT_COMMON_DIR GIT_INDEX_FILE GIT_OBJECT_DIRECTORY
unset GIT_ALTERNATE_OBJECT_DIRECTORIES GIT_CONFIG_PARAMETERS GIT_CONFIG_COUNT
unset GIT_TEMPLATE_DIR GIT_NAMESPACE GIT_CONFIG GIT_SHALLOW_FILE
git init -q --bare --initial-branch=main "$demo/remote.git"
git clone -q "$demo/remote.git" "$demo/main"
cd "$demo/main"
git config user.name 'Demo operator'
git config user.email 'demo@example.invalid'
git config commit.gpgsign false
git config tsuzuri.remote origin
git config tsuzuri.owner developer-laptop
# This fixed UUID is for isolated demos only; choose a new one for a real store.
cat > .tsuzuri.toml <<'CONFIG'
schema = 1
repository_id = "0198d2de-18c7-7ca0-a327-e6b4760f8613"
task_root = "tasks/projects"
base_branch = "main"
claim_namespace = "tsuzuri/tasks"
worktree_root = ".."
worktree_template = "wt-{task_id}"
areas = ["core"]
risks = []
CONFIG
mkdir -p tasks/projects/search/tasks
cat > tasks/projects/search/project.md <<'PROJECT'
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
PROJECT
cat > tasks/projects/search/tasks/improve-search.task.md <<'TASK'
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
TASK
git add .
git commit -qm 'Configure demo task store'
git push -q origin main
"$binary" validate
"$binary" overview
"$binary" next
"$binary" claim improve-search --worker developer-laptop
"$binary" status improve-search
printf '\nContinue in %s/wt-improve-search\n' "$demo"
printf 'This demo used only its local bare remote; no account or server was contacted.\n'
