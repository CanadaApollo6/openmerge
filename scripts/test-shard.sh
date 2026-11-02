#!/bin/sh
# Print the Go packages for one CI test shard.
#
# Each package in $heavy is a shard of its own; "rest" is every other package,
# so the shards always cover ./... exactly once. When a second argument is
# given (CI passes strategy.job-total), it must equal the number of shards
# here: a matrix that drops a shard fails instead of silently skipping tests.
set -eu

heavy="internal/store internal/serve internal/queue"
shard=${1:?usage: test-shard.sh <shard> [job-total]}

want=1
for h in $heavy; do want=$((want + 1)); done
if [ -n "${2:-}" ] && [ "$2" != "$want" ]; then
    echo "test-shard: CI matrix has $2 shards; scripts/test-shard.sh defines $want ($heavy rest)" >&2
    exit 1
fi

module=$(go list -m)
all=$(go list ./...)
for h in $heavy; do
    if ! printf '%s\n' "$all" | grep -qx "$module/$h"; then
        echo "test-shard: $h is not a package; update the shard list" >&2
        exit 1
    fi
done

case "$shard" in
rest)
    for h in $heavy; do all=$(printf '%s\n' "$all" | grep -vx "$module/$h"); done
    printf '%s\n' "$all"
    ;;
*)
    case " $heavy " in
    *" $shard "*) printf '%s\n' "$module/$shard" ;;
    *) echo "test-shard: unknown shard $shard (want one of: $heavy rest)" >&2; exit 1 ;;
    esac
    ;;
esac
