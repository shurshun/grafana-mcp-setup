#!/usr/bin/env bash
# Retakes the README screenshots from the rendered templates.
#
#   scripts/screenshots.sh [output-dir]        # defaults to images/
#
# The pages come from TestDumpPages rather than from a running service, so the
# shots carry the documented example data instead of whatever a live stack is
# called. Everything here is pinned on purpose: the dark palette, the 1000px
# width, the 2x scale, and each page's own height.
set -euo pipefail

out=${1:-images}
chrome=${CHROME:-/Applications/Google Chrome.app/Contents/MacOS/Google Chrome}
width=1000

[ -x "$chrome" ] || { echo "no Chrome at $chrome; set CHROME" >&2; exit 1; }

root=$(cd "$(dirname "$0")/.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"; [ -n "${server:-}" ] && kill "$server" 2>/dev/null || true' EXIT

DUMP_DIR="$work" go test -run TestDumpPages "$root/internal/server" >/dev/null

python3 - "$work" <<'PY'
import pathlib, sys

work = pathlib.Path(sys.argv[1])

# The environment variant is the issued page with the second storage mode
# chosen, so a static capture has to click it on load — before the height is
# measured, because that mode is a page taller.
(work / 'environment.html').write_text(
    (work / 'issued.html').read_text()
    + '\n<script>document.querySelector(\'[data-storage="env"]\').click();</script>\n'
)

for page in work.glob('*.html'):
    html = page.read_text()
    # Dark is the palette the README uses, and a headless browser has no system
    # preference to read, so the query is dropped rather than trusted.
    html = html.replace('@media (prefers-color-scheme: dark)', '@media all')
    # Chrome has no full-page screenshot flag; the height is reported through
    # the title, which --dump-dom prints and the shell reads back.
    # The body carries the page's own padding, while scrollHeight never drops
    # below the viewport and would pad a short page with dead space.
    html += ('\n<script>document.title = '
             'Math.ceil(document.body.getBoundingClientRect().height);</script>\n')
    page.write_text(html)
PY

# A run that was killed leaves its server holding the port, so the next one
# picks whatever is free rather than waiting on a stranger.
port=${PORT:-$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()')}

(cd "$work" && python3 -m http.server "$port" --bind 127.0.0.1 >/dev/null 2>&1) &
server=$!
until curl -sf -o /dev/null "http://127.0.0.1:$port/landing.html"; do sleep 0.2; done

shoot() {
  local page=$1 target=$2 url="http://127.0.0.1:$port/$1.html" height

  height=$("$chrome" --headless=new --disable-gpu --window-size="$width,800" \
    --virtual-time-budget=2000 --dump-dom "$url" 2>/dev/null |
    sed -n 's/.*<title>\([0-9]*\)<\/title>.*/\1/p' | head -1)
  [ -n "$height" ] || { echo "could not measure $page" >&2; exit 1; }

  "$chrome" --headless=new --disable-gpu --hide-scrollbars \
    --force-device-scale-factor=2 --window-size="$width,$height" \
    --virtual-time-budget=2000 --screenshot="$root/$out/$target.png" "$url" >/dev/null 2>&1
  echo "$target.png  ${width}x${height} at 2x"
}

mkdir -p "$root/$out"
shoot landing landing
shoot issued token
shoot active status
shoot environment environment
