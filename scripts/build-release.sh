#!/bin/sh
# Build the release archives for one version.
#
# usage: build-release.sh <version> <out-dir>
#
# Writes one openmerge_<version>_<os>_<arch>.tar.gz per target and a
# checksums.txt into <out-dir>. Each binary carries <version> and the commit
# it was built from, so `openmerge version` reports both and the binary can
# attest to its revision. A dirty tree is refused: the commit would not
# describe the code in the binary.
#
# The commit comes from the linker flag, not from Go's own VCS stamp. Go
# marks a build as modified when the tree has untracked files, and <out-dir>
# may be inside the tree, so every target after the first would look dirty.
set -eu

version=${1:?usage: build-release.sh <version> <out-dir>}
out=${2:?usage: build-release.sh <version> <out-dir>}
targets="linux/amd64 linux/arm64 darwin/amd64 darwin/arm64"

case $version in
    v[0-9]*) ;;
    *) echo "build-release: version must look like v1.2.3, got $version" >&2; exit 1 ;;
esac
if [ -n "$(git status --porcelain)" ]; then
    echo "build-release: the working tree is dirty; commit or stash first" >&2
    exit 1
fi

revision=$(git rev-parse HEAD)
ldflags="-s -w -X main.version=$version"
ldflags="$ldflags -X github.com/CanadaApollo6/openmerge/internal/authority.BuildRevision=$revision"

mkdir -p "$out"
for target in $targets; do
    os=${target%/*}
    arch=${target#*/}
    name="openmerge_${version#v}_${os}_${arch}"
    mkdir "$out/$name"
    CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -buildvcs=false -ldflags "$ldflags" \
        -o "$out/$name/openmerge" ./cmd/openmerge
    cp LICENSE NOTICE README.md "$out/$name/"
    tar -C "$out" -czf "$out/$name.tar.gz" "$name"
    rm -r "${out:?}/$name"
done

(cd "$out" && sha256sum openmerge_*.tar.gz >checksums.txt)
