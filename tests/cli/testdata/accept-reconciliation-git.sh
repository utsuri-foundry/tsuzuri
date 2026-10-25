#!/bin/sh
# Test-only Git wrapper: mutate the fixture immediately before accept's final
# exact claim read. All paths/identities arrive as quoted environment arguments.
set -eu
# Go's adapter adds two fixed safety options before every command. Normalize
# those options for the released injection predicates; forward them unchanged.
while [ "$#" -ge 2 ] && [ "$1" = -c ]; do
    case "$2" in
        core.hooksPath=/dev/null|protocol.ext.allow=never) shift 2 ;;
        *) break ;;
    esac
done
claim_ref=refs/heads/tsuzuri/tasks/first-task
assignment_ref=refs/heads/tsuzuri/tasks-assignments/first-task
if [ "$#" -eq 4 ] && [ "$1" = ls-remote ] && [ "$2" = --refs ]; then
    if [ "$4" = "$claim_ref" ]; then
        test ! -f "$TSUZURI_TEST_ROOT/selected"
        selected=$("$TSUZURI_TEST_GIT" -C "$TSUZURI_TEST_REMOTE" rev-parse "$assignment_ref")
        printf '%s\n' "$selected" > "$TSUZURI_TEST_ROOT/selected"
        case "$TSUZURI_TEST_FAULT" in
            worker|pending)
                tree=$("$TSUZURI_TEST_GIT" -C "$TSUZURI_TEST_REMOTE" rev-parse "$selected^{tree}")
                {
                    printf '%s\n' 'tsuzuri assignment first-task' '' \
                        'Tsuzuri-Assignment-Version: 1' \
                        "Tsuzuri-Repository: $TSUZURI_TEST_REPOSITORY" \
                        'Tsuzuri-Task: first-task' \
                        "Tsuzuri-Claim: $TSUZURI_TEST_CLAIM" \
                        'Tsuzuri-Assignment: 10000000-0000-4000-8000-000000000001'
                    if [ "$TSUZURI_TEST_FAULT" = worker ]; then
                        printf '%s\n' 'Tsuzuri-State: assigned' 'Tsuzuri-Worker: worker-b'
                    else
                        printf '%s\n' 'Tsuzuri-State: handoff-pending'
                    fi
                    printf '%s\n' "Tsuzuri-Previous: $selected" 'Tsuzuri-Updated-At: 2026-10-24T12:00:00Z'
                } > "$TSUZURI_TEST_ROOT/successor-message"
                successor=$("$TSUZURI_TEST_GIT" -C "$TSUZURI_TEST_REMOTE" -c commit.gpgSign=false \
                    commit-tree "$tree" -p "$selected" < "$TSUZURI_TEST_ROOT/successor-message")
                "$TSUZURI_TEST_GIT" -C "$TSUZURI_TEST_REMOTE" update-ref "$assignment_ref" "$successor" "$selected"
                printf '%s\n' "$successor" > "$TSUZURI_TEST_ROOT/successor"
                ;;
            absent)
                "$TSUZURI_TEST_GIT" -C "$TSUZURI_TEST_REMOTE" update-ref -d "$assignment_ref" "$selected"
                ;;
            unreadable|malformed|unchanged) ;;
            *) exit 98 ;;
        esac
    elif [ "$4" = "$assignment_ref" ] && [ -f "$TSUZURI_TEST_ROOT/selected" ]; then
        printf 'read\n' > "$TSUZURI_TEST_ROOT/final-assignment-read"
        case "$TSUZURI_TEST_FAULT" in
            unreadable)
                printf 'injected exact assignment read failure\n' >&2
                exit 19
                ;;
            malformed)
                printf 'invalid exact ref output\n'
                exit 0
                ;;
        esac
    fi
fi
exec "$TSUZURI_TEST_GIT" -c core.hooksPath=/dev/null -c protocol.ext.allow=never "$@"
