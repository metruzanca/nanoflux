#!/usr/bin/env bash
# Vendors the pinned hls.js build into internal/web/static/hls.light.min.js.
# Run with `mise run vendor:hls`.
#
# hls.js ships a self-contained UMD build (dist/hls.light.min.js) that sets
# window.Hls when loaded as a classic script, so it can be committed and served
# like any other static asset — no npm at runtime. The light build is used: it
# covers VOD playback (which is all nanoflux needs) and is much smaller than the
# full build.
set -euo pipefail

HLS_VERSION="1.7.3"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
OUT="$ROOT/internal/web/static/hls.light.min.js"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

echo "vendoring hls.js @ $HLS_VERSION (light build)"
cd "$WORK"
npm init -y >/dev/null 2>&1
npm install --no-audit --no-fund --silent "hls.js@$HLS_VERSION"

cp node_modules/hls.js/dist/hls.light.min.js "$OUT"
# Drop the sourcemap reference: the .map is not vendored, so the browser would
# otherwise 404 it whenever devtools is open.
sed -i '/^\/\/# sourceMappingURL=/d' "$OUT"
echo "wrote $OUT ($(stat -c%s "$OUT") bytes)"
