#!/usr/bin/env python3
"""Builds the guide pages from docs/guide/src.

Each page in src/ is copied to docs/guide/ with the shared stylesheet and the
diagrams from docs/diagrams/ inlined, so each result is one self-contained
file that can be published as it is.

    python3 docs/diagrams/build.py   # only when a diagram changed
    python3 docs/guide/build.py
"""
import os
import re

HERE = os.path.dirname(os.path.abspath(__file__))
SRC = os.path.join(HERE, "src")
DIAGRAMS = os.path.join(HERE, "..", "diagrams")

with open(os.path.join(SRC, "style.css")) as f:
    style = f.read()


def diagram(match):
    with open(os.path.join(DIAGRAMS, match.group(1) + ".svg")) as f:
        return f.read()


for name in sorted(os.listdir(SRC)):
    if not name.endswith(".html"):
        continue
    with open(os.path.join(SRC, name)) as f:
        page = f.read()
    page = page.replace("/*style*/", style)
    page = re.sub(r"<!--diagram:([a-z0-9-]+)-->", diagram, page)
    with open(os.path.join(HERE, name), "w") as f:
        f.write(page)
    print(f"{name}: {len(page) // 1024} KB")
