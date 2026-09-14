#!/usr/bin/env bash
# Renders images/social-preview.png, the card GitHub shows when the repository
# is linked somewhere.
#
#   scripts/social-preview.sh [output-file]
#
# GitHub asks for 1280x640; this shoots that at 2x so the text stays sharp in a
# feed, and stays under the 1MB upload limit. Upload it by hand under
# Settings -> Social preview; GitHub does not read it from the repository.
set -euo pipefail

out=${1:-images/social-preview.png}
chrome=${CHROME:-/Applications/Google Chrome.app/Contents/MacOS/Google Chrome}

[ -x "$chrome" ] || { echo "no Chrome at $chrome; set CHROME" >&2; exit 1; }

root=$(cd "$(dirname "$0")/.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# The marks come out of icons.go rather than being copied into the card, so the
# preview cannot show a client mark the page itself no longer uses.
python3 - "$root" "$work" <<'CARD'
import pathlib, re, sys

root, work = (pathlib.Path(a) for a in sys.argv[1:3])
icons = (root / "internal" / "server" / "icons.go").read_text()
card = (root / "scripts" / "social-preview.html").read_text()

def mark(name):
    body = re.search(rf'\ticon{name} = template\.HTML\(`(.*?)`\)', icons, re.S)
    if not body:
        raise SystemExit(f"icons.go has no icon{name}")
    svg = body.group(1)
    # Bigger than in the page, and painted with the colour its segment carries.
    svg = svg.replace('width="15" height="15"', 'width="22" height="22"')
    return svg.replace('currentColor', 'var(--brand)').replace('stroke="var(--brand)"', 'stroke="var(--brand)"')

card = re.sub(r'\{\{icon:(\w+)\}\}', lambda m: mark(m.group(1)), card)
(work / "card.html").write_text(card)
CARD

mkdir -p "$(dirname "$root/$out")"
"$chrome" --headless=new --disable-gpu --hide-scrollbars \
  --force-device-scale-factor=2 --window-size=1280,640 \
  --screenshot="$root/$out" "file://$work/card.html" >/dev/null 2>&1

size=$(wc -c < "$root/$out")
echo "$out  2560x1280  $((size / 1024))KB"
[ "$size" -lt 1000000 ] || echo "warning: over GitHub's 1MB limit" >&2
