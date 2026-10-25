#!/bin/sh
# Run through make release-candidate (ten-minute deadline).
set -eu
fail() { printf '%s\n' "$*" >&2; exit 1; }
version=${1-}
printf '%s\n' "$version" | LC_ALL=C grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+-rc\.[0-9]+$' || fail 'VERSION must be a prerelease, e.g. 0.1.0-rc.1'
root=$(git rev-parse --show-toplevel)
cd "$root"
test -z "$(git status --porcelain --untracked-files=all)" || fail 'release inputs must be committed and clean'
commit=$(git rev-parse HEAD)
tree=$(git rev-parse 'HEAD^{tree}')
source_version=$(git show HEAD:query.go | sed -n 's/^const Version = "\([^"]*\)"$/\1/p')
test "$version" = "$source_version" || fail 'VERSION does not match committed CLI Version'
export GOTOOLCHAIN=local GOENV=off GOFLAGS= GOWORK=off GOEXPERIMENT= CGO_ENABLED=0
export GOAMD64=v1 GOARM64=v8.0
compiler=$(go env GOVERSION)
test "$compiler" = "go$(git show HEAD:.go-version)" || fail 'compiler does not match committed .go-version'
destination="$root/build/releases/v$version-$commit"
test ! -e "$destination" || fail 'candidate already exists; retain it or explicitly move it before rebuilding'
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
mkdir "$scratch/source" "$scratch/artifacts"
# Export only committed source: ignored files cannot enter the build.
git archive --format=tar "$commit" > "$scratch/source.tar"
tar -xf "$scratch/source.tar" -C "$scratch/source"
cd "$scratch/source"
go mod download
go mod verify
go mod tidy -diff
test "$(go list -m -f '{{.GoVersion}}')" = "${compiler#go}" || fail 'module Go floor differs from compiler pin'
artifacts="$scratch/artifacts"
cp LICENSE NOTICE "$artifacts/"
{
    printf 'candidate=v%s\nsource_commit=%s\nsource_tree=%s\ncompiler=%s\n' "$version" "$commit" "$tree" "$compiler"
    printf 'CGO_ENABLED=0\nGOAMD64=v1\nGOARM64=v8.0\nflags=-mod=readonly -buildvcs=false -trimpath\n'
    printf 'status=local prerelease preparation; unsigned, not published or installed\n'
    printf '\nModule graph:\n'
    go list -m all
} > "$artifacts/BUILD.txt"
{
    printf 'Go runtime/standard library:\n\n'
    cat docs/licenses/go.txt
    for module in github.com/pelletier/go-toml/v2 github.com/yuin/goldmark; do
        printf '\n%s:\n\n' "$module"
        cat "$(go list -m -f '{{.Dir}}' "$module")/LICENSE"
    done
} > "$artifacts/THIRD_PARTY_NOTICES.txt"
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
    os=${target%/*}; arch=${target#*/}
    name="tsuzuri-v$version-$os-$arch"
    GOOS=$os GOARCH=$arch go build -mod=readonly -buildvcs=false -trimpath -o "$artifacts/$name" ./cmd/tsuzuri
    (cd "$artifacts"; go version -m "$name") >> "$artifacts/BUILD.txt"
done
host=$(go env GOHOSTOS)-$(go env GOHOSTARCH)
binary="$artifacts/tsuzuri-v$version-$host"
test -x "$binary" || fail "unsupported build host: $host"
test "$("$binary" --version)" = "tsuzuri $version" || fail 'native artifact version mismatch'
# Compatibility discovery is independent of a configured repository.
"$binary" compatibility --json > "$artifacts/compatibility.json"
grep -F "\"cliVersion\": \"$version\"" "$artifacts/compatibility.json" >/dev/null || fail 'compatibility version mismatch'
(cd "$artifacts"; sha256sum LICENSE NOTICE BUILD.txt THIRD_PARTY_NOTICES.txt compatibility.json tsuzuri-* > SHA256SUMS; sha256sum -c SHA256SUMS)
# Nothing appears in the final directory until every build and check succeeds.
mkdir -p "$(dirname "$destination")"
mv -T "$artifacts" "$destination"
printf 'Prepared %s\n' "$destination"
