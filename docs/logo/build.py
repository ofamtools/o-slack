#!/usr/bin/env python3
"""Write assets/icon.svg, assets/logo-light.svg and assets/logo-dark.svg.

The mark is a speech bubble with a channel hash knocked out of it, in
green: a message in a channel. It is the family's own drawing, not Slack's
logo or colours. The bubble's edges and the hash's strokes sit on the
8-unit grid (one pixel at 16 px), so the icon stays a bubble at 16 px. The
set, its geometry and the wordmark are o-gh's (docs/logo/build.py there,
from o-proj's, itself from ~/hub/o-plans/icons/build.py):

- icon.svg: a 128x128 tile (rx 28) in a dark tint of the tool's hue, the
  mark centred on it, in the dark palette.
- logo-light.svg, logo-dark.svg: the mark without its tile at x 0..88, then
  the name as a wordmark from x 112, drawn in the family's monoline
  lettering (s and l are new here, drawn with the same pieces).

Regenerate from the repository root:

    python3 docs/logo/build.py            # writes assets/*.svg
    python3 docs/logo/build.py --check    # the glyphs redraw ochazuke's and matomeru's wordmarks
    python3 docs/logo/build.py --preview DIR   # also renders 16, 32 and 128 px PNGs
                                               # and a contact sheet (needs rsvg-convert, magick)
"""
import pathlib
import subprocess
import sys

TOOL = "o-slack"
ROOT = pathlib.Path(__file__).resolve().parents[2]
ASSETS = ROOT / "assets"

# Palette: tile, deep, primary, light, pale (green; the family's other
# hues are oden amber, o-ai blue, o-inventory red, o-proj teal and o-gh violet).
TILE, DEEP, PRI, LIGHT, PALE = "#0f2618", "#237a45", "#45bf72", "#9fe3b6", "#e6f8ec"
R = 'stroke-linecap="round" stroke-linejoin="round"'


def mark(theme):
    """The bubble: a rounded square x 16..112, y 20..92 (rx 24) with a tail
    down and left to (28, 112); the hash's two stems lean like a written #,
    its bars at y 46 and 66. In 128-tile coordinates."""
    body = PRI if theme == "dark" else DEEP
    return (f'<path d="M40 20H88A24 24 0 0 1 112 44V68A24 24 0 0 1 88 92H56L30 106L34 90A24 24 0 0 1 16 68V44A24 24 0 0 1 40 20Z" fill="{body}"/>\n'
            f'<path d="M58 34L52 78M78 34L72 78M40 46H90M38 66H88" fill="none" stroke="{PALE}" stroke-width="8" {R}/>')


TEXT = {"light": TILE, "dark": "#ecf8f0"}

# --- the family wordmark -------------------------------------------------
# Each glyph is (width, path builder at x). Shapes are copied from the
# ochazuke and matomeru logos (o, c, h, a, z, u, k, e, m, t, r); i, n, v,
# p, j, y, l, s and the hyphen are drawn with the same pieces.
STROKE, GAP, START, BASE_H, DESC_H = 11, 15, 112, 96, 112


def f(v):
    s = f"{v:.1f}"
    return s[:-2] if s.endswith(".0") else s


GLYPHS = {
    "o": (40, lambda x: f"M{f(x+20)} 44a20 20 0 1 0 0.01 0"),
    "c": (34.1, lambda x: f"M{f(x+34.1)} 49.9A20 20 0 1 0 {f(x+34.1)} 78.1"),
    "h": (36, lambda x: f"M{f(x)} 18V84M{f(x)} 62a18 18 0 0 1 36 0V84"),
    "a": (40, lambda x: f"M{f(x+20)} 44a20 20 0 1 0 0.01 0M{f(x+40)} 44V84"),
    "z": (34, lambda x: f"M{f(x)} 44h34L{f(x)} 84h34"),
    "u": (36, lambda x: f"M{f(x)} 44V66a18 18 0 0 0 36 0M{f(x+36)} 44V84"),
    "k": (32, lambda x: f"M{f(x)} 18V84M{f(x+31)} 44L{f(x)} 70M{f(x+11)} 61L{f(x+32)} 84"),
    "e": (40, lambda x: f"M{f(x)} 64H{f(x+40)}A20 20 0 1 0 {f(x+34.1)} 78.1"),
    "m": (52, lambda x: f"M{f(x)} 44V84M{f(x)} 57a13 13 0 0 1 26 0V84M{f(x+26)} 57a13 13 0 0 1 26 0V84"),
    "t": (23, lambda x: f"M{f(x+7)} 26V73a11 11 0 0 0 11 11h4M{f(x)} 44H{f(x+23)}"),
    "r": (24, lambda x: f"M{f(x)} 44V84M{f(x)} 64a20 20 0 0 1 20 -20h4"),
    "i": (0, lambda x: f"M{f(x)} 44V84M{f(x)} 24h0.01"),
    "n": (36, lambda x: f"M{f(x)} 44V84M{f(x)} 62a18 18 0 0 1 36 0V84"),
    "v": (34, lambda x: f"M{f(x)} 44L{f(x+17)} 84L{f(x+34)} 44"),
    "-": (16, lambda x: f"M{f(x)} 64h16"),  # as in o-ai's logo
    # l is h's stem alone, spaced like i.
    "l": (0, lambda x: f"M{f(x)} 18V84"),
    # s is two half ellipses (a's bowl squashed), the top one open to the right.
    "s": (30, lambda x: f"M{f(x+30)} 50A15 10 0 0 0 {f(x+15)} 44A15 10 0 0 0 {f(x+15)} 64A15 10 0 0 1 {f(x+15)} 84A15 10 0 0 1 {f(x)} 78"),
    # Descenders reach y 106; the hook is t's foot turned down and left.
    "p": (40, lambda x: f"M{f(x)} 44V106M{f(x+20)} 44a20 20 0 1 0 0.01 0"),
    # j is spaced like i, by its stem: its hook tucks under the letter before.
    "j": (0, lambda x: f"M{f(x)} 44V95a11 11 0 0 1 -11 11h-4M{f(x)} 24h0.01"),
    "y": (36, lambda x: f"M{f(x)} 44V66a18 18 0 0 0 36 0M{f(x+36)} 44V95a11 11 0 0 1 -11 11h-4"),
    # g is a's bowl and stem with y's descender hook, reaching under the bowl.
    "g": (40, lambda x: f"M{f(x+20)} 44a20 20 0 1 0 0.01 0M{f(x+40)} 44V95a11 11 0 0 1 -11 11h-14"),
}
DESCENDERS = set("pjyg")


def wordmark(name):
    x, parts = START, []
    for ch in name:
        w, d = GLYPHS[ch]
        parts.append(d(x))
        x += w + GAP
    right = x - GAP
    width = int(right + STROKE / 2 + 3.5)  # ochazuke 509.1 -> 518, matomeru 524 -> 533
    height = DESC_H if DESCENDERS & set(name) else BASE_H
    return "".join(parts), width, height


def head(w, h):
    return (f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {w} {h}" width="{w}" height="{h}" '
            f'role="img" aria-label="{TOOL}">\n<title>{TOOL}</title>\n')


def icon():
    return (head(128, 128) + f'<rect width="128" height="128" rx="28" fill="{TILE}"/>\n'
            f'<g>\n{mark("dark")}\n</g>\n</svg>\n')


def logo(theme):
    d, w, h = wordmark(TOOL)
    # The mark's centre (64, 64) lands at (44, 48), where ochazuke's and
    # matomeru's marks sit in their logos.
    return (head(w, h) + f'<g transform="translate(-20 -16)">\n{mark(theme)}\n</g>\n'
            f'<path d="{d}" fill="none" stroke="{TEXT[theme]}" stroke-width="{STROKE}" {R}/>\n</svg>\n')


def preview(out):
    out.mkdir(parents=True, exist_ok=True)
    pngs = []
    for size in (16, 32, 128):
        p = out / f"icon-{size}.png"
        subprocess.run(["rsvg-convert", "-w", str(size), "-h", str(size), "-o", p, ASSETS / "icon.svg"], check=True)
        pngs.append(p)
    for theme, bg in (("light", "white"), ("dark", "#0d1117")):
        p = out / f"logo-{theme}.png"
        subprocess.run(["rsvg-convert", "-w", "720", "-b", bg, "-o", p, ASSETS / f"logo-{theme}.svg"], check=True)
    # 16 and 32 px also shown blown up 8x and 4x (nearest neighbour), to see the pixels.
    big = []
    for size, k in ((16, 8), (32, 4)):
        b = out / f"icon-{size}-x{k}.png"
        subprocess.run(["magick", out / f"icon-{size}.png", "-filter", "point", "-resize", f"{k*100}%", b], check=True)
        big.append(b)
    subprocess.run(["magick", "-background", "#f6f3ee", *map(str, pngs), *map(str, big), "-bordercolor", "#f6f3ee",
                    "-border", "12", "-gravity", "center", "+append", out / "icons.png"], check=True)
    subprocess.run(["magick", out / "logo-light.png", out / "logo-dark.png", "-append", out / "logos.png"], check=True)
    print(out / "icons.png")
    print(out / "logos.png")


def check():
    """The glyph table must redraw ochazuke's and matomeru's wordmarks exactly."""
    import re
    ok = True
    for fam in ("ochazuke", "matomeru"):
        f = pathlib.Path.home() / f"proj/{fam}/assets/logo-light.svg"
        if not f.exists():
            print(f"skip {fam}: {f} not found")
            continue
        t = f.read_text()
        want = re.findall(r'<path d="(M[^"]*)" fill="none" stroke="#[0-9a-f]+" stroke-width="11"', t)[-1]
        want_w = int(re.search(r'viewBox="0 0 (\d+) 96"', t).group(1))
        d, w, _ = wordmark(fam)
        split = lambda s: re.sub(r"([A-Za-z])", r" \1 ", s).split()
        a, b = split(d), split(want)
        # Numbers may differ by 0.1 from rounding (ochazuke's e ends at 503.3, x + 34.2).
        near = lambda u, v: u == v or (u[0] not in "MmAaHhVvLlZz" and abs(float(u) - float(v)) < 0.15)
        same = len(a) == len(b) and all(map(near, a, b)) and w == want_w
        ok &= same
        print(f"{fam}: {'same' if same else 'DIFFERENT'} wordmark ({w} wide)")
    return ok


def main():
    if sys.argv[1:] == ["--check"]:
        sys.exit(0 if check() else 1)
    ASSETS.mkdir(exist_ok=True)
    (ASSETS / "icon.svg").write_text(icon())
    for theme in ("light", "dark"):
        (ASSETS / f"logo-{theme}.svg").write_text(logo(theme))
    for n in ("icon.svg", "logo-light.svg", "logo-dark.svg"):
        print(ASSETS / n)
    if len(sys.argv) == 3 and sys.argv[1] == "--preview":
        preview(pathlib.Path(sys.argv[2]))


if __name__ == "__main__":
    main()
