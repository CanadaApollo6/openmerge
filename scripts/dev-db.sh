#!/bin/sh
set -eu

data=${OPENMERGE_DEV_PGDATA:-/tmp/om-pg}
container=openmerge-postgres
image=postgres:18.6-bookworm@sha256:afc7e2d441324c0388fa80c3d24f733b4194a4eb7f47dd8ee2b08eb1a24a647c

case "${1:-}" in
start)
    if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
        echo 'Using Docker (Postgres 18.6).'
        if ! docker inspect "$container" >/dev/null 2>&1; then
            docker run --rm -d --name "$container" -p 127.0.0.1:54329:5432 \
                -e POSTGRES_USER=postgres -e POSTGRES_PASSWORD=openmerge \
                --health-cmd='pg_isready -U postgres' --health-interval=1s \
                --health-timeout=5s --health-retries=30 "$image"
        fi
        tries=0
        until docker exec "$container" pg_isready -U postgres >/dev/null 2>&1; do
            tries=$((tries + 1))
            if [ "$tries" -ge 30 ]; then
                docker logs "$container"
                exit 1
            fi
            sleep 1
        done
        echo 'export OPENMERGE_TEST_DATABASE_URL=postgres://postgres:openmerge@localhost:54329/postgres?sslmode=disable'
    else
        echo 'Using mise (user-owned Postgres 18.6; Docker is unavailable without sudo).'
        mise install postgres@18.6
        pg=$(mise where postgres@18.6)
        if [ ! -f "$data/PG_VERSION" ]; then
            "$pg/bin/initdb" -D "$data" -U postgres --auth=trust
        fi
        if ! "$pg/bin/pg_ctl" -D "$data" status >/dev/null 2>&1; then
            "$pg/bin/pg_ctl" -D "$data" -o '-h localhost -p 54329 -k /tmp' -l "$data.log" -w start
        fi
        echo 'export OPENMERGE_TEST_DATABASE_URL=postgres://postgres@localhost:54329/postgres?sslmode=disable'
    fi
    ;;
stop)
    if [ -f "$data/postmaster.pid" ]; then
        pg=$(mise where postgres@18.6)
        "$pg/bin/pg_ctl" -D "$data" -w stop
    elif command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1 && docker inspect "$container" >/dev/null 2>&1; then
        docker stop "$container"
    else
        echo 'No development Postgres is running.'
    fi
    ;;
*)
    echo 'Usage: dev-db.sh start|stop' >&2
    exit 2
    ;;
esac
