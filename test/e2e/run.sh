#!/bin/sh
set -eu
[ $# -ge 1 ] || { echo "usage: AWGX_PASSWORD=... $0 USER@HOST [playwright args]" >&2; exit 2; }
: "${AWGX_PASSWORD:?set AWGX_PASSWORD to the web UI password}"
host=$1
shift
cd "$(dirname "$0")"
listen=$(ssh -o BatchMode=yes "$host" sudo awgx web status | sed -n 's/^listen: *//p')
[ -n "$listen" ] || { echo "$host: web UI listen address not found" >&2; exit 1; }
port=${AWGX_PORT:-17457}
ctl="${TMPDIR:-/tmp}/awgx-e2e-$port"
ssh -f -N -o BatchMode=yes -o ExitOnForwardFailure=yes -M -S "$ctl" -L "$port:$listen" "$host"
trap 'ssh -S "$ctl" -O exit "$host" 2>/dev/null' EXIT
[ -d node_modules ] || npm ci
AWGX_SSH=$host AWGX_URL=http://127.0.0.1:$port npx playwright test "$@"
