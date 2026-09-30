"""Pin variable-font axes that <canvas> can't reach, keeping wght variable.

canvas 2D can set weight (numeric) but not opsz / ROND / wdth, so we cut
instances: Big Shoulders @ opsz 72, Doto @ ROND 100 (round LED dots),
Martian Mono @ wdth 75 / 100 / 112.5.
"""
from fontTools.ttLib import TTFont
from fontTools.varLib import instancer
import os

NM = "node_modules/@fontsource-variable"
OUT = "src/fonts"
jobs = [
    (f"{NM}/big-shoulders/files/big-shoulders-latin-opsz-normal.woff2", {"opsz": 72}, "BigShoulders-opsz72.woff2"),
    (f"{NM}/doto/files/doto-latin-full-normal.woff2", {"ROND": 100}, "Doto-round.woff2"),
    (f"{NM}/martian-mono/files/martian-mono-latin-standard-normal.woff2", {"wdth": 75}, "MartianMono-cond.woff2"),
    (f"{NM}/martian-mono/files/martian-mono-latin-standard-normal.woff2", {"wdth": 100}, "MartianMono-norm.woff2"),
    (f"{NM}/martian-mono/files/martian-mono-latin-standard-normal.woff2", {"wdth": 112.5}, "MartianMono-wide.woff2"),
]
os.makedirs(OUT, exist_ok=True)
for src, loc, name in jobs:
    f = TTFont(src)
    inst = instancer.instantiateVariableFont(f, loc)
    inst.flavor = "woff2"
    inst.save(f"{OUT}/{name}")
    axes = [a.axisTag for a in inst["fvar"].axes] if "fvar" in inst else []
    print(name, "remaining axes:", axes, os.path.getsize(f"{OUT}/{name}"), "bytes")
