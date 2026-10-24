#!/usr/bin/env bash
# Inert until period B drains. Requires Bash, GNU date/find, flock and Python 3.
set -euo pipefail
set +x
umask 077
fail() { echo "audit-anchor: $*" >&2; exit 1; }
trap 'echo "audit-anchor: failed; no successful anchor assumed; retain outputs for investigation" >&2' ERR
dry=false weekly=false
for arg in "$@"; do
    case "$arg" in
        --dry-run) dry=true ;;
        --weekly) weekly=true ;;
        *) fail 'usage: audit-anchor-daily.sh [--dry-run] [--weekly]; see operating.md for environment' ;;
    esac
done
[[ -n ${OPENMERGE_DATABASE_URL:-} && -n ${AUDIT_REPO:-} && -n ${AUDIT_INSTALLATION:-} &&
   -n ${AUDIT_OUTPUT_BASE:-} && -n ${AUDIT_ANCHOR_CHECKOUT:-} ]] || fail 'missing required environment'
[[ $AUDIT_REPO =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ && $AUDIT_INSTALLATION =~ ^[1-9][0-9]*$ ]] || fail 'invalid scope'
# Require an explicit, operator-established maximum commit lag in seconds.
[[ ${AUDIT_LAG_SECONDS:-} =~ ^[0-9]{1,8}$ ]] || fail 'set AUDIT_LAG_SECONDS to the established lag bound'
bin=$(command -v "${OPENMERGE_BIN:-openmerge}") || fail 'binary unavailable'
verifier=$(cd -- "$(dirname -- "$0")" && pwd)/verify-audit-anchor.py
checkout=$(cd -- "$AUDIT_ANCHOR_CHECKOUT" && pwd)
gitdir=$(git -C "$checkout" rev-parse --absolute-git-dir)
exec 8>"$gitdir/audit-anchor.lock"
flock -n 8 || fail 'another anchor run is active'
[[ -z $(git -C "$checkout" status --porcelain) ]] || fail 'anchor checkout must be clean'
scope=${AUDIT_REPO//\//_}-$AUDIT_INSTALLATION
mkdir -p -- "$AUDIT_OUTPUT_BASE/$scope"
base=$(cd -- "$AUDIT_OUTPUT_BASE/$scope" && pwd)
receipts="receipts-$scope.tsv"
until=$(date -u +%FT00:00:00Z)
now=$(date -u +%s)
(( now - $(date -u -d "$until" +%s) >= 10#$AUDIT_LAG_SECONDS )) || fail 'window has not passed the lag bound'
days=1
$weekly && days=7
from=$(date -u -d "$until -$days days" +%FT00:00:00Z)
revision=$("$bin" version | sed -n 's/^commit: //p')
[[ $revision =~ ^[0-9a-f]{40,64}$ ]] || fail 'binary must embed a known revision'
run=$(mktemp -d "$base/run-XXXXXXXX")
# Parse the complete two-line contract, including the final LF, without shell trimming.
printed_digest() {
    python3 - "$1" "$2" <<'PY'
import pathlib, re, sys
raw = pathlib.Path(sys.argv[1]).read_bytes()
m = re.fullmatch(re.escape(sys.argv[2].encode()) + rb'\nmanifest SHA-256: ([0-9a-f]{64})\n', raw)
if not m:
    sys.exit('audit-anchor: invalid audit stdout')
print(m[1].decode())
PY
}
replay() {
    local original=$1 target=$2 expected=$3 actual file
    "$bin" audit export --manifest "$original/manifest.json" --out "$target" >"$target.stdout" 2>"$target.stderr"
    actual=$(printed_digest "$target.stdout" "$target")
    [[ $actual == "$expected" ]] || fail 'manifest digest mismatch'
    for file in manifest.json events.jsonl selection.jsonl dependency_boundaries.jsonl watermarks.jsonl; do
        cmp -s -- "$original/$file" "$target/$file" || fail 'replay files differ'
    done
}
"$bin" audit export --repo "$AUDIT_REPO" --installation "$AUDIT_INSTALLATION" \
    --from "$from" --until "$until" --out "$run/export" >"$run/export.stdout" 2>"$run/export.stderr"
digest=$(printed_digest "$run/export.stdout" "$run/export")
replay "$run/export" "$run/replay" "$digest"
if $weekly; then
    # Require the last seven receipts to cover exactly the seven previous UTC days.
    python3 - "$checkout/$receipts" "$from" "$until" "$AUDIT_REPO" "$AUDIT_INSTALLATION" >"$run/week.tsv" <<'PY'
import datetime as dt, pathlib, re, sys
try:
    rows = pathlib.Path(sys.argv[1]).read_text().splitlines()[-7:]
    assert len(rows) == 7
    start = dt.datetime.fromisoformat(sys.argv[2])
    for i, row in enumerate(rows):
        a, b, repo, installation, digest, revision, bundle = row.split('\t')
        assert a == (start + dt.timedelta(days=i)).strftime('%Y-%m-%dT00:00:00Z')
        assert b == (start + dt.timedelta(days=i+1)).strftime('%Y-%m-%dT00:00:00Z')
        assert (repo, installation) == tuple(sys.argv[4:6])
        assert re.fullmatch('[0-9a-f]{64}', digest)
        assert re.fullmatch('run-[A-Za-z0-9]+', bundle)
        print(row)
except Exception:
    sys.exit('audit-anchor: invalid or missing seven-day receipts')
PY
    while IFS=$'\t' read -r a b repo installation anchor rev bundle; do
        earlier="$base/$bundle/export"
        target="$run/$bundle-replay"
        replay "$earlier" "$target" "$anchor"
        printf '%s\n' "$anchor" >"$run/digest.txt"
        python3 "$verifier" --anchor "$run/digest.txt" --replay-stdout "$target.stdout" \
            --replay-out "$target" --earlier "$earlier" --later "$run/export" --require-full-coverage
    done <"$run/week.tsv"
else
    # Retrying a published day requires operator review rather than a second receipt.
    if [[ -f $checkout/$receipts ]] && awk -F '\t' -v day="$from" '$1 == day { found=1 } END { exit !found }' "$checkout/$receipts"; then
        fail 'day already receipted; inspect existing anchor'
    fi
    printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$from" "$until" "$AUDIT_REPO" \
        "$AUDIT_INSTALLATION" "$digest" "$revision" "${run##*/}" >>"$checkout/$receipts"
    if ! $dry; then
        git -C "$checkout" add -- "$receipts"
        git -C "$checkout" commit -m "Anchor $scope $from to $until" >/dev/null 2>&1 || fail 'receipt commit failed'
        git -C "$checkout" push >/dev/null 2>&1 || fail 'receipt push failed; resolve locally before retrying'
    fi
fi
# Dry runs retain every bundle; prune only this script's scoped run directories.
if ! $dry; then
    find "$base" -mindepth 1 -maxdepth 1 -type d -name 'run-*' -mmin +129600 -exec rm -rf -- {} +
fi
echo 'audit-anchor: verified; dry-run skips commit, push and pruning when requested'
