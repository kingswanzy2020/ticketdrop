#!/usr/bin/env python3
"""Builds ticketdrop-explainer.mp4, the animated walkthrough of the platform.

Every frame is drawn with pycairo and piped to ffmpeg, and the narration is
spoken by Piper, an offline text-to-speech engine, so the video is
reproducible: change a scene or its narration below and rebuild.

    python3 docs/video/build.py --voice en_US-lessac-medium.onnx
    python3 docs/video/build.py --silent           # pictures and captions only
    python3 docs/video/build.py --still 42 a.png   # one frame, at second 42

Needs python3-cairo, the Noto Sans and Noto Sans Mono fonts, and ffmpeg: on
PATH, named by $FFMPEG, or from `pip install imageio-ffmpeg`. The narration
also needs `pip install piper-tts` and a voice (the .onnx file and the
.onnx.json beside it), named by --voice or $PIPER_VOICE. Voices are at
https://huggingface.co/rhasspy/piper-voices; this one is
en/en_US/lessac/medium.

A scene plays at its own pace where the narration is shorter than the
pictures, and slows down where the narration needs longer.

Every number shown on screen comes from docs/PROOF.md or docs/DECISIONS.md.
Change it there first.
"""
import argparse
import math
import os
import random
import shutil
import subprocess
import sys
import tempfile
import wave
from array import array
from concurrent.futures import ProcessPoolExecutor

import cairo

W, H, FPS = 1920, 1080, 30
SANS, MONO = "Noto Sans", "Noto Sans Mono"
HERE = os.path.dirname(os.path.abspath(__file__))


def rgb(h):
    return tuple(int(h[i:i + 2], 16) / 255 for i in (1, 3, 5))


BG, BG2 = rgb("#0c1018"), rgb("#121a2a")
PANEL, PANEL2, LINE = rgb("#161e2e"), rgb("#1e283b"), rgb("#2f3c56")
INK, MUTED, DIM = rgb("#eef2f8"), rgb("#9aa7bf"), rgb("#5d6a83")
AMBER, TEAL, CORAL, BLUE = rgb("#f6b93b"), rgb("#3fd0b0"), rgb("#ff6b6b"), rgb("#6eadff")


# --------------------------------------------------------------- helpers

def seg(t, a, b):
    """Eased 0..1 as t goes from a to b."""
    if t <= a:
        return 0.0
    if t >= b:
        return 1.0
    x = (t - a) / (b - a)
    return x * x * (3 - 2 * x)


def lerp(a, b, p):
    return a + (b - a) * p


def mix(c1, c2, p):
    return tuple(lerp(a, b, p) for a, b in zip(c1, c2))


def src(ctx, col, a=1.0):
    ctx.set_source_rgba(col[0], col[1], col[2], a)


def font(ctx, size, bold=False, mono=False):
    ctx.select_font_face(MONO if mono else SANS, cairo.FONT_SLANT_NORMAL,
                         cairo.FONT_WEIGHT_BOLD if bold else cairo.FONT_WEIGHT_NORMAL)
    ctx.set_font_size(size)


def runs(s):
    """Splits s around the arrows, which the Noto faces lack and DejaVu Sans has."""
    out, cur = [], ""
    for ch in s:
        if ch == "→":
            out += [(cur, False), (ch, True)]
            cur = ""
        else:
            cur += ch
    return [r for r in out + [(cur, False)] if r[0]]


def width(ctx, s, size, bold=False, mono=False):
    w = 0
    for part, arrow in runs(s):
        font(ctx, size, bold, mono)
        if arrow:
            ctx.select_font_face("DejaVu Sans", cairo.FONT_SLANT_NORMAL, cairo.FONT_WEIGHT_NORMAL)
        w += ctx.text_extents(part).x_advance
    return w


def text(ctx, s, x, y, size, col=INK, a=1.0, bold=False, mono=False, align="l", maxw=None):
    """Draws s with its baseline at y. Shrinks to maxw rather than overflow."""
    if a <= 0.004:
        return 0
    w = width(ctx, s, size, bold, mono)
    if maxw and w > maxw:
        size *= maxw / w
        w = width(ctx, s, size, bold, mono)
    if align == "c":
        x -= w / 2
    elif align == "r":
        x -= w
    src(ctx, col, a)
    ctx.move_to(x, y)
    for part, arrow in runs(s):
        font(ctx, size, bold, mono)
        if arrow:
            ctx.select_font_face("DejaVu Sans", cairo.FONT_SLANT_NORMAL, cairo.FONT_WEIGHT_NORMAL)
        ctx.show_text(part)
    return w


def rrect(ctx, x, y, w, h, r):
    r = min(r, w / 2, h / 2)
    ctx.new_sub_path()
    ctx.arc(x + w - r, y + r, r, -math.pi / 2, 0)
    ctx.arc(x + w - r, y + h - r, r, 0, math.pi / 2)
    ctx.arc(x + r, y + h - r, r, math.pi / 2, math.pi)
    ctx.arc(x + r, y + r, r, math.pi, 1.5 * math.pi)
    ctx.close_path()


def box(ctx, x, y, w, h, fill=PANEL, stroke=LINE, a=1.0, r=14, lw=2, dash=None):
    if a <= 0.004:
        return
    rrect(ctx, x, y, w, h, r)
    if fill:
        src(ctx, fill, a)
        ctx.fill_preserve()
    if stroke:
        src(ctx, stroke, a)
        ctx.set_line_width(lw)
        if dash:
            ctx.set_dash(dash)
        ctx.stroke_preserve()
        ctx.set_dash([])
    ctx.new_path()


def pill(ctx, s, cx, cy, col, a=1.0, size=18, mono=False, filled=False):
    """A rounded label centred on (cx, cy)."""
    if a <= 0.004:
        return
    w = width(ctx, s, size, True, mono) + size * 1.5
    h = size * 1.9
    box(ctx, cx - w / 2, cy - h / 2, w, h, fill=col if filled else mix(BG, col, 0.16),
        stroke=col, a=a, r=h / 2, lw=1.5)
    text(ctx, s, cx, cy + size * 0.35, size, BG if filled else col, a, bold=True, mono=mono, align="c")


def check(ctx, cx, cy, s, col=TEAL, a=1.0, p=1.0):
    if a <= 0.004 or p <= 0:
        return
    pts = [(-0.5, 0.0), (-0.12, 0.38), (0.55, -0.42)]
    src(ctx, col, a)
    ctx.set_line_width(max(3, s * 0.16))
    ctx.set_line_cap(cairo.LINE_CAP_ROUND)
    ctx.set_line_join(cairo.LINE_JOIN_ROUND)
    ctx.move_to(cx + pts[0][0] * s, cy + pts[0][1] * s)
    q = min(1, p * 2)
    ctx.line_to(cx + lerp(pts[0][0], pts[1][0], q) * s, cy + lerp(pts[0][1], pts[1][1], q) * s)
    if p > 0.5:
        q = (p - 0.5) * 2
        ctx.line_to(cx + lerp(pts[1][0], pts[2][0], q) * s, cy + lerp(pts[1][1], pts[2][1], q) * s)
    ctx.stroke()
    ctx.set_line_cap(cairo.LINE_CAP_BUTT)


def cross(ctx, cx, cy, s, col=CORAL, a=1.0):
    if a <= 0.004:
        return
    src(ctx, col, a)
    ctx.set_line_width(max(3, s * 0.16))
    ctx.set_line_cap(cairo.LINE_CAP_ROUND)
    for d in (1, -1):
        ctx.move_to(cx - s * 0.4, cy - s * 0.4 * d)
        ctx.line_to(cx + s * 0.4, cy + s * 0.4 * d)
    ctx.stroke()
    ctx.set_line_cap(cairo.LINE_CAP_BUTT)


def cylinder(ctx, cx, cy, w, h, col, a=1.0, fill=PANEL):
    """A database."""
    if a <= 0.004:
        return
    e = h * 0.16
    ctx.set_line_width(2)
    ctx.save()
    ctx.translate(cx, cy)
    ctx.move_to(-w / 2, -h / 2 + e)
    ctx.line_to(-w / 2, h / 2 - e)
    ctx.save()
    ctx.translate(0, h / 2 - e)
    ctx.scale(w / 2, e)
    ctx.arc_negative(0, 0, 1, math.pi, 0)
    ctx.restore()
    ctx.line_to(w / 2, -h / 2 + e)
    ctx.close_path()
    src(ctx, fill, a)
    ctx.fill_preserve()
    src(ctx, col, a)
    ctx.stroke()
    ctx.save()
    ctx.translate(0, -h / 2 + e)
    ctx.scale(w / 2, e)
    ctx.arc(0, 0, 1, 0, 2 * math.pi)
    ctx.restore()
    src(ctx, mix(fill, col, 0.25), a)
    ctx.fill_preserve()
    src(ctx, col, a)
    ctx.stroke()
    ctx.restore()


def ticket(ctx, x, y, w, h, col, a=1.0, fill=None, lw=3):
    """A ticket outline: a rounded rectangle with a notch in each side."""
    if a <= 0.004:
        return
    r, n = h * 0.12, h * 0.14
    ctx.new_sub_path()
    ctx.arc(x + w - r, y + r, r, -math.pi / 2, 0)
    ctx.arc_negative(x + w, y + h / 2, n, -math.pi / 2, math.pi / 2)
    ctx.arc(x + w - r, y + h - r, r, 0, math.pi / 2)
    ctx.arc(x + r, y + h - r, r, math.pi / 2, math.pi)
    ctx.arc_negative(x, y + h / 2, n, math.pi / 2, -math.pi / 2)
    ctx.arc(x + r, y + r, r, math.pi, 1.5 * math.pi)
    ctx.close_path()
    if fill:
        src(ctx, fill, a)
        ctx.fill_preserve()
    src(ctx, col, a)
    ctx.set_line_width(lw)
    ctx.stroke()


def line(ctx, p0, p1, col, a=1.0, lw=3, dash=None, p=1.0, head=False):
    """A line from p0 towards p1, drawn as far as p, with an optional arrowhead."""
    if a <= 0.004 or p <= 0:
        return p0
    tip = (lerp(p0[0], p1[0], p), lerp(p0[1], p1[1], p))
    src(ctx, col, a)
    ctx.set_line_width(lw)
    if dash:
        ctx.set_dash(dash)
    ctx.move_to(*p0)
    ctx.line_to(*tip)
    ctx.stroke()
    ctx.set_dash([])
    if head:
        ang = math.atan2(p1[1] - p0[1], p1[0] - p0[0])
        s = 13
        ctx.move_to(*tip)
        ctx.line_to(tip[0] - s * math.cos(ang - 0.42), tip[1] - s * math.sin(ang - 0.42))
        ctx.line_to(tip[0] - s * math.cos(ang + 0.42), tip[1] - s * math.sin(ang + 0.42))
        ctx.close_path()
        ctx.fill()
    return tip


def dot(ctx, p, r, col, a=1.0, glow=True):
    if a <= 0.004:
        return
    if glow:
        src(ctx, col, a * 0.22)
        ctx.arc(p[0], p[1], r * 2.2, 0, 2 * math.pi)
        ctx.fill()
    src(ctx, col, a)
    ctx.arc(p[0], p[1], r, 0, 2 * math.pi)
    ctx.fill()


def bolt(ctx, cx, cy, s, col=CORAL, a=1.0):
    if a <= 0.004:
        return
    pts = [(0.15, -1), (-0.55, 0.12), (-0.05, 0.12), (-0.2, 1), (0.55, -0.2), (0.05, -0.2)]
    ctx.move_to(cx + pts[0][0] * s, cy + pts[0][1] * s)
    for px, py in pts[1:]:
        ctx.line_to(cx + px * s, cy + py * s)
    ctx.close_path()
    src(ctx, col, a)
    ctx.fill()


# ---------------------------------------------------------------- chrome

def header(ctx, n, title):
    text(ctx, f"{n:02d}", 80, 86, 22, AMBER, bold=True, mono=True)
    text(ctx, title, 124, 86, 24, MUTED, bold=True)
    ticket(ctx, 1644, 63, 38, 26, AMBER, lw=2)
    text(ctx, "TicketDrop", 1840, 86, 24, MUTED, bold=True, align="r")


def captions(ctx, t, items, end):
    """The two lines under every scene: the same point in plain and technical terms.

    items is [(start, plain, technical)]; each stays up until the next starts.
    """
    box(ctx, 60, 856, 1800, 172, fill=PANEL, stroke=LINE, r=18, lw=1.5)
    for i, (start, plain, tech) in enumerate(items):
        stop = items[i + 1][0] if i + 1 < len(items) else end + 1
        a = seg(t, start, start + 0.45) * (1 - seg(t, stop - 0.3, stop))
        if a <= 0.004:
            continue
        dy = (1 - seg(t, start, start + 0.45)) * 10
        pill(ctx, "IN PLAIN TERMS", 196, 908 + dy, AMBER, a, size=15)
        text(ctx, plain, 330, 920 + dy, 33, INK, a, maxw=1490)
        pill(ctx, "UNDER THE HOOD", 196, 978 + dy, BLUE, a, size=15)
        text(ctx, tech, 330, 988 + dy, 25, mix(INK, BLUE, 0.35), a, maxw=1490)


# ------------------------------------------------------------- 1. title

def scene_title(ctx, t, D):
    p = seg(t, 0.2, 1.3)
    tw, th = 560 * lerp(0.9, 1, p), 250 * lerp(0.9, 1, p)
    ticket(ctx, W / 2 - tw / 2 - 180, 250 - th / 2 + 190, tw + 360, th, AMBER, a=p, fill=mix(BG, AMBER, 0.06), lw=4)
    text(ctx, "TicketDrop", W / 2, 478, 124, INK, seg(t, 0.5, 1.5), bold=True, align="c")
    text(ctx, "An event-driven ticketing platform", W / 2, 650, 42, MUTED, seg(t, 1.2, 2.0), align="c")
    chips = ["9 Go services", "PostgreSQL", "SNS + SQS", "Docker Compose", "Prometheus"]
    x = W / 2 - (len(chips) - 1) * 135
    for i, c in enumerate(chips):
        a = seg(t, 1.8 + i * 0.18, 2.3 + i * 0.18)
        pill(ctx, c, x + i * 270, 740 + (1 - a) * 12, BLUE, a, size=22)
    captions(ctx, t, [
        (2.2, "A system for selling tickets fairly when a crowd turns up all at once.",
         "Nine Go services that talk through events over SNS and SQS, run locally with Docker Compose."),
    ], D)


# ----------------------------------------------------------- 2. problem

_rng = random.Random(7)
CROWD = [(_rng.uniform(0, 1), _rng.uniform(0, 1), _rng.uniform(0, 2.6), _rng.uniform(0, 6.28))
         for _ in range(170)]
PROMISES = [
    ("Never oversell", "No ticket is ever sold twice"),
    ("Never lose an order", "Even if a service dies mid-sale"),
    ("Never charge twice", "Even if a message is repeated"),
    ("Never leave an order stuck", "Every order ends: ticketed or failed"),
]


def scene_problem(ctx, t, D):
    header(ctx, 1, "THE PROBLEM")
    dim = 1 - 0.86 * seg(t, 6.0, 7.0)
    # the crowd pours in from the left and piles up against the booth
    for u, v, delay, ph in CROWD:
        p = seg(t, 0.3 + delay, 2.6 + delay)
        tx = 190 + u ** 0.6 * 900
        ty = 210 + v * 540
        x = lerp(-60 - u * 500, tx, p) + math.sin(t * 2.2 + ph) * 5
        y = ty + math.cos(t * 1.9 + ph) * 5
        dot(ctx, (x, y), 9, BLUE, 0.85 * dim, glow=False)
    text(ctx, "buyers", 640, 800, 26, BLUE, seg(t, 1.0, 1.8) * dim, bold=True, align="c")
    # the booth: a handful of tickets
    for i in range(12):
        a = seg(t, 0.4 + i * 0.05, 0.9 + i * 0.05) * dim
        ticket(ctx, 1290 + (i % 3) * 170, 250 + (i // 3) * 92, 140, 66, AMBER, a, fill=mix(BG, AMBER, 0.1))
    text(ctx, "tickets", 1530, 650, 26, AMBER, seg(t, 1.0, 1.8) * dim, bold=True, align="c")
    # and the machinery behind it is not reliable
    for i, name in enumerate(["service", "service", "service"]):
        a = seg(t, 2.6, 3.2) * dim
        down = i == 1 and 3.6 < t < 5.6 and int(t * 4) % 2 == 0
        col = CORAL if (i == 1 and 3.6 < t < 5.6) else MUTED
        box(ctx, 1290 + i * 170, 716, 140, 56, fill=mix(BG, CORAL, 0.2) if down else PANEL, stroke=col, a=a, r=10)
        text(ctx, "crashed" if (i == 1 and 3.6 < t < 5.6) else name, 1360 + i * 170, 751, 19, col, a, mono=True,
             align="c")
    bolt(ctx, 1256, 744, 22, CORAL, seg(t, 3.6, 3.8) * (1 - seg(t, 5.4, 5.8)) * dim)

    for i, (title, sub) in enumerate(PROMISES):
        a = seg(t, 7.0 + i * 0.7, 7.7 + i * 0.7)
        x, y = 80 + i * 445, 330 + (1 - a) * 20
        box(ctx, x, y, 420, 250, fill=PANEL, stroke=TEAL, a=a, r=18)
        src(ctx, mix(PANEL, TEAL, 0.18), a)
        ctx.arc(x + 62, y + 66, 32, 0, 2 * math.pi)
        ctx.fill()
        check(ctx, x + 62, y + 66, 30, TEAL, a, seg(t, 7.4 + i * 0.7, 8.0 + i * 0.7))
        text(ctx, title, x + 30, y + 152, 31, INK, a, bold=True, maxw=362)
        text(ctx, sub, x + 30, y + 198, 22, MUTED, a, maxw=362)
    captions(ctx, t, [
        (0.3, "A ticket drop: far more buyers than tickets, and everyone arrives in the same second.",
         "A burst of concurrent orders against a small, fixed stock, while services crash and restart."),
        (6.6, "Whatever goes wrong, the platform makes four promises to every buyer.",
         "Correctness and recoverability come first; every design choice follows from these four."),
    ], D)


# ---------------------------------------------------------- 3. services

SERVICES = [
    ("web", "The shop window", "Server-rendered Go templates + htmx", False),
    ("gateway", "The front door", "Reverse proxy with a route allowlist", False),
    ("catalog", "The event listing", "Owns drops, tiers, prices, capacities", True),
    ("orders", "The order desk", "Tracks each order to ticketed or failed", True),
    ("inventory", "The stockroom", "Holds, confirms and releases tickets", True),
    ("payments", "The till", "Charges through a simulated provider", True),
    ("fulfillment", "The ticket printer", "Issues the tickets", True),
    ("notifications", "The messenger", "Tells the customer how it ended", False),
    ("scheduler", "The clock", "Opens drops, expires unpaid holds", False),
]


def scene_services(ctx, t, D):
    header(ctx, 2, "NINE SERVICES, ONE JOB EACH")
    cw, ch, gx, gy = 573, 196, 30, 24
    for i, (name, nick, tech, has_db) in enumerate(SERVICES):
        a = seg(t, 0.5 + i * 0.85, 1.1 + i * 0.85)
        x = 80 + (i % 3) * (cw + gx)
        y = 130 + (i // 3) * (ch + gy) + (1 - a) * 16
        db = seg(t, 11.5 + i * 0.12, 12.2 + i * 0.12) if has_db else 0
        box(ctx, x, y, cw, ch, fill=PANEL, stroke=mix(LINE, TEAL, db), a=a, r=16)
        text(ctx, name, x + 28, y + 58, 32, INK, a, bold=True, mono=True)
        text(ctx, nick, x + 28, y + 108, 28, AMBER, a, bold=True)
        text(ctx, tech, x + 28, y + 154, 21, MUTED, a, maxw=cw - 150)
        cylinder(ctx, x + cw - 62, y + 62, 58, 62, TEAL, a * db, fill=PANEL2)
        text(ctx, "own DB", x + cw - 62, y + 124, 16, TEAL, a * db, bold=True, align="c")
    captions(ctx, t, [
        (0.4, "Nine small programs, each with one job, like the staff of a box office.",
         "One Go module: nine services under services/ sharing a platform library in pkg/platform."),
        (11.2, "Each keeps its own records. Nobody reaches into anyone else's filing cabinet.",
         "A PostgreSQL database per service: no shared tables and no cross-service transactions."),
    ], D)


# ------------------------------------------------------------ 4. events

QUEUES = ["payments", "inventory", "fulfillment", "orders", "notifications"]


def scene_events(ctx, t, D):
    header(ctx, 3, "HOW THE SERVICES TALK")
    # publisher: the change and the event are written together
    box(ctx, 80, 300, 330, 300, fill=PANEL, stroke=LINE, r=16)
    text(ctx, "orders", 108, 348, 28, INK, bold=True, mono=True)
    text(ctx, "a publishing service", 108, 378, 18, MUTED)
    box(ctx, 104, 400, 282, 176, fill=None, stroke=AMBER, a=seg(t, 0.6, 1.2), r=10, lw=1.5, dash=[7, 6])
    text(ctx, "one transaction", 245, 425, 16, AMBER, seg(t, 0.6, 1.2), bold=True, align="c")
    for i, (lab, col) in enumerate([("order row", INK), ("outbox row", BLUE)]):
        a = seg(t, 1.0, 1.6)
        box(ctx, 122, 440 + i * 62, 246, 50, fill=PANEL2, stroke=col, a=a, r=8, lw=1.5)
        text(ctx, lab, 245, 472 + i * 62, 20, col, a, mono=True, align="c")
    # relay
    box(ctx, 500, 400, 170, 100, fill=PANEL, stroke=LINE, r=14)
    text(ctx, "relay", 585, 446, 26, INK, bold=True, mono=True, align="c")
    text(ctx, "the postman", 585, 476, 18, AMBER, align="c")
    line(ctx, (414, 450), (494, 450), MUTED, lw=2.5, head=True)
    # topic
    box(ctx, 760, 250, 230, 400, fill=mix(BG, BLUE, 0.09), stroke=BLUE, r=18)
    text(ctx, "SNS topic", 875, 530, 28, INK, bold=True, align="c")
    text(ctx, "ticketdrop-events", 875, 564, 18, BLUE, mono=True, align="c")
    text(ctx, "the noticeboard", 875, 600, 20, AMBER, align="c")
    line(ctx, (674, 450), (754, 450), MUTED, lw=2.5, head=True)
    # queues and consumers
    qa = seg(t, 5.6, 6.6)
    ys = [190 + i * 130 for i in range(5)]

    def lit(i):
        """How strongly queue i is picked out by the event now on the wire."""
        a1 = seg(t, 8.6, 9.2) * (1 - seg(t, 12.6, 13.0)) if i == 0 else 0
        a2 = seg(t, 15.2, 15.8) if i in (1, 3) else 0
        return max(a1, a2)

    for i, q in enumerate(QUEUES):
        y, hi = ys[i], lit(i)
        col = mix(LINE, TEAL, hi)
        line(ctx, (994, 450), (1150, y + 45), mix(DIM, TEAL, hi), a=qa, lw=2 + hi, dash=[8, 7])
        box(ctx, 1156, y, 300, 90, fill=PANEL, stroke=col, a=qa, r=12)
        for k in range(4):   # slots, to read as a queue
            box(ctx, 1170 + k * 28, y + 50, 22, 26, fill=mix(PANEL2, TEAL, hi * (k == 0)), stroke=col, a=qa, r=4,
                lw=1)
        text(ctx, f"{q}-events", 1170, y + 34, 19, mix(MUTED, INK, hi), qa, mono=True)
        text(ctx, "SQS queue + DLQ", 1442, y + 72, 15, DIM, qa, align="r")
        line(ctx, (1460, y + 45), (1536, y + 45), col, a=qa, lw=2.5, head=True)
        box(ctx, 1542, y, 298, 90, fill=PANEL, stroke=col, a=qa, r=12)
        text(ctx, q, 1691, y + 42, 24, mix(MUTED, INK, max(hi, 0.4)), qa, bold=True, mono=True, align="c",
             maxw=270)
        text(ctx, "events.Once", 1691, y + 70, 16, DIM, qa, mono=True, align="c")

    def travel(label, t0, targets, col):
        """One event: outbox -> relay -> topic -> the queues subscribed to it."""
        pa, pb, pc = seg(t, t0, t0 + 1.2), seg(t, t0 + 1.3, t0 + 2.3), seg(t, t0 + 2.6, t0 + 3.8)
        gone = 1 - seg(t, t0 + 4.6, t0 + 5.0)
        if pa <= 0 or gone <= 0:
            return
        if pc <= 0:
            x = lerp(414, 494, pa) if pb <= 0 else lerp(674, 754, pb)
            if pa < 1 or 0 < pb < 1:
                dot(ctx, (x, 450), 11, col, gone)
            lx = lerp(245, 585, pa) if pb <= 0 else lerp(585, 875, pb)
            pill(ctx, label, lx, lerp(372, 300, pb), col, gone, size=17, mono=True)
        else:
            pill(ctx, label, 875, 300, col, gone, size=17, mono=True)
            for i in targets:
                p0, p1, p2 = (994, 450), (1150, ys[i] + 45), (1691, ys[i] + 45)
                q = pc * 2
                pos = (lerp(p0[0], p1[0], q), lerp(p0[1], p1[1], q)) if q < 1 else \
                    (lerp(p1[0], p2[0], q - 1), p1[1])
                dot(ctx, pos, 11, col, gone)

    travel("order.created", 2.0, [], BLUE) if t < 7.0 else None
    travel("order.created", 8.0, [0], TEAL)
    travel("payment.failed", 14.4, [1, 3], TEAL)
    text(ctx, "filter: event_type", 1072, 168, 18, TEAL, qa * seg(t, 8.0, 8.6), mono=True, align="c")
    captions(ctx, t, [
        (0.4, "Services don't phone each other. They pin a note to a shared noticeboard.",
         "The event is an outbox row; a relay publishes it to one SNS topic, at least once."),
        (7.4, "Each service has its own mailbox, and only gets the notes it asked for.",
         "One SQS queue per consumer, subscribed with a filter on the event_type message attribute."),
        (14.0, "A note can go to several mailboxes. If a service is down, its mail just waits.",
         "Fan-out without coupling: a backlog or outage in one consumer does not slow the others."),
    ], D)


# ----------------------------------------------------------- 5. journey

NW, NH = 190, 80
NODES = {
    "customer": (150, 250, "the buyer"),
    "web": (390, 250, "shop window"),
    "gateway": (630, 250, "front door"),
    "orders": (900, 250, "order desk"),
    "payments": (1420, 250, "the till"),
    "inventory": (1420, 560, "stockroom"),
    "fulfillment": (900, 560, "ticket printer"),
    "notifications": (400, 560, "messenger"),
    "scheduler": (1770, 250, "the clock"),
    "catalog": (1770, 560, "event listing"),
}
# id: (from, to, label, is an event, label offset from the midpoint, label align, sideways shift)
EDGES = {
    "sched_cat": ("scheduler", "catalog", "open-due · 1 s", False, (-14, -30), "r", 0),
    "cat_inv": ("catalog", "inventory", "drop.opened", True, (0, -20), "c", 0),
    "cust_web": ("customer", "web", "", False, (0, 0), "c", 0),
    "web_gw": ("web", "gateway", "POST /v1/orders", False, (0, -66), "c", 0),
    "gw_ord": ("gateway", "orders", "", False, (0, 0), "c", 0),
    "hold": ("orders", "inventory", "hold tickets", False, (0, -12), "c", 0),
    "ord_pay": ("orders", "payments", "order.created", True, (0, -20), "c", -14),
    "pay_inv": ("payments", "inventory", "payment.succeeded", True, (18, 40), "l", 0),
    "inv_ful": ("inventory", "fulfillment", "order.confirmed", True, (0, -20), "c", 0),
    "ful_ord": ("fulfillment", "orders", "ticket.issued", True, (18, 60), "l", 0),
    "ord_not": ("orders", "notifications", "order.ticketed", True, (0, 6), "c", 0),
    "not_cust": ("notifications", "customer", "message", False, (-20, 6), "r", 0),
    # the two unhappy endings
    "pay_ord_x": ("payments", "orders", "payment.failed", True, (0, 34), "c", -14),
    "pay_inv_x": ("payments", "inventory", "payment.failed", True, (18, 40), "l", 0),
    "ord_not_x": ("orders", "notifications", "order.failed", True, (0, 6), "c", 0),
    "sched_inv": ("scheduler", "inventory", "expire · 5 s", False, (60, -62), "c", 0),
    "inv_ord_x": ("inventory", "orders", "hold.expired", True, (-130, 14), "c", 16),
}


def node(ctx, name, hot=0.0, col=TEAL, a=1.0, label=None):
    x, y, nick = NODES[name]
    box(ctx, x - NW / 2, y - NH / 2, NW, NH, fill=mix(PANEL, col, 0.13 * hot), stroke=mix(LINE, col, hot), a=a,
        r=14, lw=2 + hot)
    text(ctx, label or name, x, y - 2, 22, mix(MUTED, INK, max(hot, 0.55)), a, bold=True, mono=True, align="c",
         maxw=NW - 16)
    text(ctx, nick, x, y + 24, 16, mix(DIM, AMBER, max(hot, 0.5)), a, align="c")


def edge_points(eid):
    a, b, _, _, _, _, shift = EDGES[eid]
    (ax, ay, _), (bx, by, _) = NODES[a], NODES[b]
    d = math.hypot(bx - ax, by - ay)
    ux, uy = (bx - ax) / d, (by - ay) / d

    def leave(cx, cy, sx, sy):
        k = min((NW / 2 + 7) / abs(sx) if sx else 1e9, (NH / 2 + 7) / abs(sy) if sy else 1e9)
        return cx + sx * k - uy * shift, cy + sy * k + ux * shift

    return leave(ax, ay, ux, uy), leave(bx, by, -ux, -uy)


def edge(ctx, eid, p, col, a=1.0, live=False, labelled=True):
    """Draws edge eid as far as p; a live edge carries a moving dot at its tip."""
    if p <= 0 or a <= 0.004:
        return
    _, _, label, is_event, (dx, dy), align, _ = EDGES[eid]
    p0, p1 = edge_points(eid)
    tip = line(ctx, p0, p1, col, a, lw=3, dash=[10, 7] if is_event else None, p=p, head=p > 0.97)
    if live and p < 0.97:
        dot(ctx, tip, 9, col, a)
    if label and labelled:
        la = a * seg(p, 0.25, 0.7)
        mx, my = (p0[0] + p1[0]) / 2 + dx, (p0[1] + p1[1]) / 2 + dy
        w = width(ctx, label, 18, True, True)
        x0 = mx - w / 2 if align == "c" else (mx - w if align == "r" else mx)
        box(ctx, x0 - 8, my - 22, w + 16, 30, fill=BG, stroke=None, a=la * 0.92, r=6)
        text(ctx, label, x0, my, 18, col, la, bold=True, mono=True)


def legend(ctx, a=1.0):
    line(ctx, (90, 720), (160, 720), MUTED, a, lw=3, head=True)
    text(ctx, "HTTP call (waits for the answer)", 178, 727, 19, MUTED, a)
    line(ctx, (90, 762), (160, 762), MUTED, a, lw=3, dash=[10, 7], head=True)
    text(ctx, "event through SNS and SQS (delivered later)", 178, 769, 19, MUTED, a)


STEP = 3.6
JOURNEY = [  # (edges drawn in turn, nodes lit, plain, technical)
    (["sched_cat", "cat_inv"], ["scheduler", "catalog", "inventory"],
     "At the opening time the clock starts the sale, and the stockroom counts in the tickets.",
     "scheduler → catalog open-due → drop.opened → inventory creates stock, with no button to press."),
    (["cust_web", "web_gw", "gw_ord"], ["customer", "web", "gateway", "orders"],
     "A customer places an order in the shop.",
     "web → gateway → orders: POST /v1/orders. The gateway forwards only explicitly listed routes."),
    (["hold"], ["orders", "inventory"],
     "Before saying yes, the order desk asks the stockroom to set the tickets aside.",
     "A synchronous HTTP hold: 409 if sold out, 404 if not on sale, 503 if inventory is unreachable."),
    (["ord_pay"], ["orders", "payments"],
     "The order is written down as pending, and a note goes out: new order.",
     "The order row and an order.created outbox row are committed in one transaction."),
    (["pay_inv"], ["payments", "inventory"],
     "The till takes the payment and reports that it went through.",
     "payments consumes order.created, calls the provider and emits payment.succeeded."),
    (["inv_ful"], ["inventory", "fulfillment"],
     "The stockroom turns the reservation into a sale.",
     "inventory confirms the hold and emits order.confirmed."),
    (["ful_ord"], ["fulfillment", "orders"],
     "The printer issues the tickets and tells the order desk.",
     "fulfillment consumes order.confirmed, issues the tickets and emits ticket.issued."),
    (["ord_not"], ["orders", "notifications"],
     "The order desk marks the order as ticketed and announces how it ended.",
     "pending → ticketed, with order.ticketed published in the same transaction."),
    (["not_cust"], ["notifications", "customer"],
     "The customer is told, and the shop page shows the result.",
     "notifications consumes order.ticketed; the shopfront polls the public API with htmx."),
]


def scene_journey(ctx, t, D):
    header(ctx, 4, "ONE ORDER, START TO FINISH")
    cur = min(int(t / STEP), len(JOURNEY) - 1)
    hot = {}
    for i, (edges, nodes, _, _) in enumerate(JOURNEY[:cur + 1]):
        lt = t - i * STEP
        for k, eid in enumerate(edges):
            span = 1.5 / len(edges)
            p = seg(lt, 0.3 + k * span, 0.3 + (k + 1) * span)
            edge(ctx, eid, p, TEAL if i == cur else mix(DIM, TEAL, 0.45), live=i == cur)
        if i == cur:
            for n in nodes:
                hot[n] = seg(lt, 0.2, 0.8)
    for n in NODES:
        node(ctx, n, hot.get(n, 0.0))
    # the order's status, as the customer sees it
    if cur >= 3:
        done = seg(t, 7 * STEP + 1.6, 7 * STEP + 2.1)
        a = seg(t, 3 * STEP + 0.4, 3 * STEP + 0.9)
        pill(ctx, "status: ticketed" if done > 0.5 else "status: pending", 900, 176,
             TEAL if done > 0.5 else AMBER, a, size=18, mono=True, filled=done > 0.5)
    legend(ctx)
    pill(ctx, f"step {cur + 1} of {len(JOURNEY)}", 1770, 760, MUTED, 1, size=18)
    captions(ctx, t, [(i * STEP + (0.2 if i else 0.4), s[2], s[3]) for i, s in enumerate(JOURNEY)], D)


# ------------------------------------------------------------ 6. failure

def scene_failure(ctx, t, D):
    header(ctx, 5, "WHEN IT DOES NOT GO TO PLAN")
    for eid in ("cust_web", "web_gw", "gw_ord", "hold", "ord_pay"):   # what already happened
        edge(ctx, eid, 1, DIM, 0.55, labelled=eid != "hold")
    first = 1 - seg(t, 7.6, 8.2)       # the declined payment
    second = seg(t, 8.0, 8.6)          # the payment that never comes
    edge(ctx, "pay_inv_x", seg(t, 1.0, 2.2), CORAL, first, live=True)
    edge(ctx, "pay_ord_x", seg(t, 1.0, 2.2), CORAL, first, live=True)
    edge(ctx, "ord_not_x", seg(t, 3.4, 4.6), CORAL, first, live=True)
    edge(ctx, "not_cust", seg(t, 4.9, 5.9), CORAL, first, live=True)
    edge(ctx, "sched_inv", seg(t, 9.0, 10.2), AMBER, second, live=True)
    edge(ctx, "inv_ord_x", seg(t, 10.6, 11.8), CORAL, second, live=True)
    edge(ctx, "ord_not_x", seg(t, 12.3, 13.3), CORAL, second, live=True)
    edge(ctx, "not_cust", seg(t, 13.5, 14.4), CORAL, second, live=True)

    for n in NODES:
        if n == "catalog":
            continue
        h, col = 0.0, CORAL
        if n == "payments":
            h = max(seg(t, 0.4, 1.0) * first, second)
        elif n in ("inventory", "orders"):
            h = max(seg(t, 2.0, 2.4) * first, seg(t, 10.0, 10.6) * second)
        elif n == "notifications":
            h = max(seg(t, 4.4, 4.8) * first, seg(t, 13.2, 13.5) * second)
        elif n == "scheduler":
            h, col = seg(t, 8.6, 9.2), AMBER
        node(ctx, n, h, col)
    pill(ctx, "card declined", 1420, 176, CORAL, seg(t, 0.4, 1.0) * first, size=18)
    pill(ctx, "provider down: no answer", 1420, 176, CORAL, second, size=18)
    pill(ctx, "tickets back in stock", 1420, 640, TEAL, max(seg(t, 2.3, 2.9) * first, seg(t, 10.4, 11.0) * second),
         size=18)
    pill(ctx, "status: failed", 900, 176, CORAL, seg(t, 2.3, 2.9) * first, size=18, mono=True, filled=True)
    pill(ctx, "status: failed · hold expired", 900, 176, CORAL, seg(t, 11.8, 12.3) * second, size=18, mono=True,
         filled=True)
    legend(ctx)
    captions(ctx, t, [
        (0.4, "If the card is declined, the tickets go straight back on the shelf and the order is closed.",
         "payment.failed: inventory releases the hold; orders goes pending → failed with a failure_reason."),
        (8.2, "If the payment never arrives at all, the reservation simply runs out.",
         "Unsettled holds are released after HOLD_GRACE; hold.expired fails the order. No charge after expiry."),
    ], D)


# ----------------------------------------------------------- 7. oversell

def scene_oversell(ctx, t, D):
    header(ctx, 6, "PROMISE: NEVER OVERSELL")
    sold = seg(t, 4.3, 4.7)
    # two buyers, one ticket
    for i, (name, y) in enumerate([("Buyer A", 300), ("Buyer B", 600)]):
        a = seg(t, 0.4 + i * 0.3, 1.0 + i * 0.3)
        src(ctx, mix(PANEL, BLUE, 0.25), a)
        ctx.arc(190, y, 56, 0, 2 * math.pi)
        ctx.fill()
        dot(ctx, (190, y - 14), 17, BLUE, a, glow=False)
        src(ctx, BLUE, a)
        ctx.arc(190, y + 44, 32, math.pi, 2 * math.pi)
        ctx.fill()
        text(ctx, name, 190, y + 96, 24, INK, a, bold=True, align="c")
        p0, p1 = (262, y), (608, 450 + (y - 450) * 0.25)
        tip = line(ctx, p0, p1, BLUE, a, lw=3, p=seg(t, 2.4, 4.2), head=seg(t, 2.4, 4.2) > 0.97)
        if 0 < seg(t, 2.4, 4.2) < 0.97:
            dot(ctx, tip, 10, BLUE, a)
        text(ctx, "hold 1 ticket", 430, y + (-62 if i == 0 else 78), 20, BLUE, a * seg(t, 2.4, 3.0), mono=True,
             align="c")
        win = i == 0
        ra = seg(t, 5.0 + i * 0.5, 5.6 + i * 0.5)
        col = TEAL if win else CORAL
        box(ctx, 60, y + 118, 500, 62, fill=mix(BG, col, 0.12), stroke=col, a=ra, r=12, lw=1.5)
        (check if win else cross)(ctx, 96, y + 149, 26, col, ra)
        text(ctx, "1 row updated: hold granted" if win else "0 rows updated: 409 sold out", 128, y + 157, 21,
             col, ra, bold=True, mono=True, maxw=418)
    # the stock counter
    a = seg(t, 0.8, 1.5)
    box(ctx, 614, 310, 360, 280, fill=PANEL, stroke=mix(AMBER, CORAL, sold), a=a, r=18, lw=3)
    text(ctx, "stock · general tier", 794, 360, 21, MUTED, a, mono=True, align="c")
    text(ctx, "1" if sold < 0.5 else "0", 794, 500, 150, mix(AMBER, CORAL, sold), a, bold=True, mono=True,
         align="c")
    text(ctx, "available", 794, 556, 24, MUTED, a, align="c")
    # the statement that decides
    a = seg(t, 6.2, 7.0)
    box(ctx, 1040, 290, 800, 320, fill=rgb("#0a0e15"), stroke=LINE, a=a, r=16)
    text(ctx, "services/inventory/internal/stock/stock.go", 1070, 336, 17, DIM, a, mono=True)
    code = [("UPDATE stock", INK), ("SET    available = available - $3", INK),
            ("WHERE  drop_id = $1 AND tier = $2", INK), ("  AND  available >= $3", AMBER)]
    for i, (ln, col) in enumerate(code):
        if i == 3:
            box(ctx, 1058, 510, 764, 50, fill=mix(BG, AMBER, 0.14), stroke=None, a=a * seg(t, 7.4, 8.0), r=8)
        text(ctx, ln, 1076, 400 + i * 48, 27, col, a, bold=i == 3, mono=True)
    text(ctx, "The check and the change are one statement.", 1440, 660, 22, AMBER, seg(t, 7.6, 8.2), align="c")
    # measured
    a = seg(t, 12.2, 13.0)
    box(ctx, 614, 690, 1226, 96, fill=mix(BG, TEAL, 0.1), stroke=TEAL, a=a, r=16)
    check(ctx, 668, 738, 34, TEAL, a, seg(t, 12.6, 13.2))
    text(ctx, "400 simultaneous hold requests for 100 tickets: exactly 100 granted.", 720, 748, 29, INK, a,
         bold=True, maxw=1090)
    captions(ctx, t, [
        (0.4, "Two people reach for the last ticket in the same instant. Who gets it?",
         "Reading the stock and then writing it would race, so there is no separate read."),
        (5.2, "The database answers yes or no in a single step, so only one of them can win.",
         "One conditional UPDATE ... WHERE available >= n; zero rows affected means sold out."),
        (12.0, "Measured: four hundred requests at once for one hundred tickets. One hundred got them.",
         "Recorded in docs/DECISIONS.md, entry 16, and asserted on every make drop-check run."),
    ], D)


# -------------------------------------------------------- 8. reliability

def scene_reliable(ctx, t, D):
    header(ctx, 7, "PROMISES: NEVER LOSE AN ORDER, NEVER CHARGE TWICE")
    starts = [0.0, 7.0, 14.0]
    titles = [("Transactional outbox", "Saved together, or not at all"),
              ("Idempotent consumers", "A repeat is recognised"),
              ("Dead-letter queue", "The unreadable is set aside")]
    for i in range(3):
        on = seg(t, starts[i] + 0.2, starts[i] + 0.9)
        nxt = starts[i + 1] if i < 2 else 1e9
        a = on * lerp(1, 0.5, seg(t, nxt, nxt + 0.6))
        x = 80 + i * 600
        ctx.push_group()
        box(ctx, x, 130, 560, 690, fill=PANEL, stroke=LINE, r=18)
        text(ctx, titles[i][0], x + 30, 186, 30, INK, bold=True)
        text(ctx, titles[i][1], x + 30, 226, 23, AMBER, bold=True)
        lt = t - starts[i]
        cx = x + 280
        if i == 0:
            box(ctx, x + 50, 270, 460, 220, fill=None, stroke=AMBER, a=seg(lt, 1.0, 1.5), r=12, lw=1.5,
                dash=[8, 6])
            text(ctx, "one database transaction", cx, 300, 18, AMBER, seg(lt, 1.0, 1.5), bold=True, align="c")
            for k, (lab, col) in enumerate([("orders: new order, pending", INK), ("outbox: order.created", BLUE)]):
                ra = seg(lt, 1.6, 2.2)
                box(ctx, x + 74, 322 + k * 74, 412, 58, fill=PANEL2, stroke=col, a=ra, r=8, lw=1.5)
                text(ctx, lab, cx, 358 + k * 74, 20, col, ra, mono=True, align="c")
            pill(ctx, "COMMIT", x + 446, 270, TEAL, seg(lt, 2.4, 2.8), size=15, filled=True)
            ba = seg(lt, 3.2, 3.4) * (1 - seg(lt, 4.4, 4.9))
            bolt(ctx, cx - 120, 540, 30, CORAL, ba)
            text(ctx, "process killed here", cx - 86, 550, 22, CORAL, ba, bold=True)
            pa = seg(lt, 4.8, 5.8)
            line(ctx, (cx, 500), (cx, 660), BLUE, seg(lt, 4.6, 4.9), lw=3, dash=[10, 7], p=pa, head=pa > 0.97)
            text(ctx, "relay publishes after restart", cx + 16, 590, 18, BLUE, seg(lt, 5.2, 5.8))
            box(ctx, cx - 150, 668, 300, 64, fill=mix(BG, BLUE, 0.1), stroke=BLUE, a=seg(lt, 4.6, 5.0), r=12)
            text(ctx, "SNS topic", cx, 708, 22, INK, seg(lt, 4.6, 5.0), bold=True, align="c")
            text(ctx, "The event is never lost.", cx, 782, 21, TEAL, seg(lt, 5.8, 6.3), bold=True, align="c")
        elif i == 1:
            box(ctx, x + 120, 560, 320, 150, fill=PANEL2, stroke=LINE, a=seg(lt, 0.8, 1.3), r=12)
            text(ctx, "processed_events", cx, 600, 21, MUTED, seg(lt, 0.8, 1.3), bold=True, mono=True, align="c")
            text(ctx, "7f3a", cx, 660, 32, TEAL, seg(lt, 2.6, 3.0), bold=True, mono=True, align="c")
            for k, (t0, col) in enumerate([(1.4, TEAL), (3.6, CORAL)]):
                p = seg(lt, t0, t0 + 1.1)
                ya = lerp(290, 470, p) if k == 0 else lerp(290, 420, p)
                xa = cx + (-110 if k == 0 else 110)
                a2 = seg(lt, t0, t0 + 0.3)
                box(ctx, xa - 100, ya - 34, 200, 68, fill=mix(BG, col, 0.12), stroke=col, a=a2, r=10)
                text(ctx, "event 7f3a", xa, ya - 2, 20, INK, a2, bold=True, mono=True, align="c")
                text(ctx, "1st delivery" if k == 0 else "2nd delivery", xa, ya + 22, 15, col, a2, align="c")
                if k == 0:
                    check(ctx, xa, 520, 24, TEAL, 1, seg(lt, 2.5, 2.9))
                else:
                    cross(ctx, xa, 492, 24, CORAL, seg(lt, 4.7, 5.0))
            text(ctx, "First: work done, ID recorded.", cx, 748, 20, TEAL, seg(lt, 2.8, 3.3), bold=True, align="c")
            text(ctx, "Second: duplicate, nothing changes.", cx, 782, 20, CORAL, seg(lt, 5.0, 5.5), bold=True,
                 align="c")
        else:
            box(ctx, x + 60, 290, 440, 90, fill=PANEL2, stroke=LINE, a=seg(lt, 0.8, 1.3), r=12)
            text(ctx, "consumer queue", cx, 344, 21, MUTED, seg(lt, 0.8, 1.3), bold=True, mono=True, align="c")
            moved = seg(lt, 4.6, 5.6)
            for k in range(3):
                fa = seg(lt, 1.4 + k, 1.7 + k)
                bx = x + 100 + k * 130
                box(ctx, bx, 420, 100, 78, fill=mix(BG, CORAL, 0.12), stroke=CORAL, a=fa * (1 - moved * 0.6), r=10,
                    lw=1.5)
                text(ctx, f"try {k + 1}", bx + 50, 452, 18, INK, fa * (1 - moved * 0.6), mono=True, align="c")
                cross(ctx, bx + 50, 476, 18, CORAL, fa * (1 - moved * 0.6))
            line(ctx, (cx, 512), (cx, 600), AMBER, seg(lt, 4.4, 4.7), lw=3, p=moved, head=moved > 0.97)
            box(ctx, x + 60, 610, 440, 110, fill=mix(BG, AMBER, 0.1), stroke=AMBER, a=seg(lt, 4.4, 4.9), r=12)
            text(ctx, "dead-letter queue", cx, 656, 22, AMBER, seg(lt, 4.4, 4.9), bold=True, mono=True, align="c")
            text(ctx, "waits for a person to look", cx, 692, 18, MUTED, seg(lt, 5.4, 5.9), align="c")
            text(ctx, "The rest of the line keeps moving.", cx, 782, 21, TEAL, seg(lt, 5.6, 6.1), bold=True,
                 align="c")
        ctx.pop_group_to_source()
        ctx.paint_with_alpha(a)
    captions(ctx, t, [
        (0.4, "The change and the note about it are saved together: both, or neither.",
         "events.Enqueue writes the outbox row in the caller's transaction; the relay publishes at least once."),
        (7.2, "If the same note arrives twice, the second copy is recognised and ignored.",
         "events.Once records the event ID in processed_events in the same transaction as the work."),
        (14.2, "A note that nobody can read is set aside after three tries, so it cannot block the line.",
         "SQS redrive policy: maxReceiveCount 3, then the consumer's own dead-letter queue."),
    ], D)


# ---------------------------------------------------------- 9. scheduler

def scene_scheduler(ctx, t, D):
    header(ctx, 8, "PROMISE: NEVER LEAVE AN ORDER STUCK")
    killed = seg(t, 12.0, 12.3)
    over = seg(t, 13.0, 13.6)            # the standby holds the lock
    reps = [(760, 300), (760, 600)]
    targets = [(1500, 300, "catalog", "open drops that are due", 1.0),
               (1500, 600, "inventory", "expire unpaid holds", 5.0)]
    cylinder(ctx, 260, 450, 170, 190, BLUE, seg(t, 0.4, 1.0))
    text(ctx, "PostgreSQL", 260, 590, 22, INK, seg(t, 0.4, 1.0), bold=True, align="c")
    text(ctx, "advisory lock", 260, 620, 19, BLUE, seg(t, 0.4, 1.0), mono=True, align="c")
    for i, (x, y) in enumerate(reps):
        dead = killed if i == 0 else 0
        lead = (1 - killed) if i == 0 else over
        a = seg(t, 0.6 + i * 0.3, 1.2 + i * 0.3)
        col = mix(mix(LINE, AMBER, lead), CORAL, dead)
        line(ctx, (350, 450), (x - 172, y), AMBER, a * lead * seg(t, 1.4, 2.0), lw=3)
        box(ctx, x - 165, y - 62, 330, 124, fill=mix(PANEL, col, 0.1), stroke=col, a=a * lerp(1, 0.55, dead), r=16,
            lw=2.5)
        text(ctx, "scheduler", x, y - 8, 27, INK, a * lerp(1, 0.5, dead), bold=True, mono=True, align="c")
        text(ctx, f"replica {i + 1}", x, y + 26, 19, MUTED, a * lerp(1, 0.5, dead), align="c")
        pill(ctx, "LEADER", x + 100, y - 62, AMBER, a * lead * seg(t, 1.6, 2.1), size=15, filled=True)
        if i == 1:
            pill(ctx, "STANDBY", x + 100, y - 62, MUTED, a * (1 - over) * seg(t, 1.6, 2.1), size=15)
        else:
            pill(ctx, "SIGKILL", x + 100, y - 62, CORAL, dead, size=15, filled=True)
            cross(ctx, x - 120, y, 34, CORAL, dead)
    for x, y, name, what, every in targets:
        a = seg(t, 1.0, 1.6)
        box(ctx, x - 190, y - 62, 380, 124, fill=PANEL, stroke=LINE, a=a, r=16)
        text(ctx, name, x, y - 8, 27, INK, a, bold=True, mono=True, align="c")
        text(ctx, what, x, y + 26, 19, MUTED, a, align="c")
        text(ctx, f"every {every:g} s", x, y - 84, 19, AMBER, a, bold=True, mono=True, align="c")
        # ticks from whichever replica leads
        for src_i, (rx, ry) in enumerate(reps):
            lead_from = 2.0 if src_i == 0 else 13.6
            lead_to = 12.0 if src_i == 0 else 99
            p0, p1 = (rx + 172, ry), (x - 197, y)
            if lead_from <= t:
                line(ctx, p0, p1, DIM, 0.6 * (1 - killed if src_i == 0 else over), lw=2, head=True)
            n = int((t - lead_from) / every) if t >= lead_from else -1
            for k in range(max(0, n - 1), n + 1):
                t0 = lead_from + k * every
                if t0 > lead_to or t < t0:
                    continue
                p = (t - t0) / 0.7
                if 0 <= p <= 1:
                    dot(ctx, (lerp(p0[0], p1[0], p), lerp(p0[1], p1[1], p)), 9, AMBER)
    text(ctx, "Both calls are safe to make twice at once, so a brief overlap between leaders does no harm.",
         W / 2, 790, 22, MUTED, seg(t, 14.4, 15.2), align="c")
    captions(ctx, t, [
        (0.4, "The clock does no work itself. It taps two services on the shoulder, on time.",
         "POSTs catalog's open-due every second and inventory's expire every five; the services do the work."),
        (12.2, "There are two clocks. If the one in charge dies, the spare takes over.",
         "Two replicas; whichever holds a Postgres advisory lock leads (scheduler_leader gauge)."),
    ], D)


# -------------------------------------------------------------- 10. proof

DROP_ROWS = [
    ("Ticketed + failed = accepted", ["368+47=415", "383+31=414", "370+43=413"]),
    ("Tickets issued, capacity 1,000", ["914", "951", "917"]),
    ("Orders with the wrong ticket count", ["0", "0", "0"]),
    ("All orders finished after the restart", ["14 s", "14 s", "17 s"]),
    ("Customers told, once each", ["415", "414", "413"]),
    ("Replayed events caught as duplicates", ["7 of 7", "7 of 7", "7 of 7"]),
]
OUTAGE_ROWS = [
    ("Standby clock took over in", "1 s"),
    ("Orders failed, reason \"hold expired\"", "5 of 5"),
    ("Tickets back in stock", "20 of 20"),
    ("Charged after the hold expired", "0"),
    ("Customers told a second time", "0"),
]


def scene_proof(ctx, t, D):
    header(ctx, 9, "PROVED BY BREAKING IT")
    a = seg(t, 0.3, 0.9)
    box(ctx, 80, 130, 1080, 600, fill=PANEL, stroke=LINE, a=a, r=18)
    text(ctx, "make drop-check", 110, 186, 30, INK, a, bold=True, mono=True)
    text(ctx, "600 orders of 1 to 4 tickets for 1,000 tickets, with one service SIGKILLed mid-run", 110, 226, 21,
         AMBER, a, maxw=1020)
    cols = [700, 870, 1040]
    text(ctx, "service killed:", 560, 280, 18, DIM, a, align="r")
    for x, name in zip(cols, ["payments", "inventory", "fulfillment"]):
        text(ctx, name, x, 280, 19, CORAL, a, bold=True, mono=True, align="c")
    for i, (lab, vals) in enumerate(DROP_ROWS):
        ra = seg(t, 1.6 + i * 1.2, 2.2 + i * 1.2)
        y = 340 + i * 66
        src(ctx, LINE, ra * 0.7)
        ctx.rectangle(110, y - 42, 1020, 1)
        ctx.fill()
        check(ctx, 126, y - 8, 22, TEAL, ra, seg(t, 1.9 + i * 1.2, 2.4 + i * 1.2))
        text(ctx, lab, 156, y, 23, INK, ra, maxw=440)
        for x, v in zip(cols, vals):
            text(ctx, v, x, y, 21, TEAL, ra, bold=True, mono=True, align="c", maxw=160)

    t2 = 11.0
    a = seg(t, t2, t2 + 0.6)
    box(ctx, 1190, 130, 650, 600, fill=PANEL, stroke=LINE, a=a, r=18)
    text(ctx, "make outage-check", 1220, 186, 30, INK, a, bold=True, mono=True)
    text(ctx, "Payment provider down, clock leader SIGKILLed", 1220, 226, 21, AMBER, a, maxw=590)
    for i, (lab, v) in enumerate(OUTAGE_ROWS):
        ra = seg(t, t2 + 1.0 + i * 1.1, t2 + 1.6 + i * 1.1)
        y = 320 + i * 82
        src(ctx, LINE, ra * 0.7)
        ctx.rectangle(1220, y - 50, 590, 1)
        ctx.fill()
        check(ctx, 1236, y - 8, 22, TEAL, ra, seg(t, t2 + 1.3 + i * 1.1, t2 + 1.8 + i * 1.1))
        text(ctx, lab, 1266, y, 23, INK, ra, maxw=400)
        text(ctx, v, 1810, y, 25, TEAL, ra, bold=True, mono=True, align="r")
    text(ctx, "Runs of 6 October 2026 at commit 3c43610, recorded in docs/PROOF.md. All four passed.", 80, 776, 21,
         MUTED, seg(t, 1.0, 1.6))
    text(ctx, "Not proven yet: a real crowd of thousands of buyers, and behaviour on Kubernetes.", 80, 812, 21,
         DIM, seg(t, 18.0, 18.8))
    captions(ctx, t, [
        (0.4, "None of this is taken on trust. A script sells out a drop, pulls the plug, then counts.",
         "Demand over supply, SIGKILL mid-run, every event replayed at every hop, a poison message to the DLQ."),
        (10.8, "Then the payment provider is taken away, and the clock in charge is killed.",
         "PAYMENT_ERROR_RATE=1 and 40-second holds, then the dead-lettered payments are redriven."),
    ], D)


# ---------------------------------------------------------- 11. platform

TODAY = [
    ("Docker Compose", "the whole stack with make up"),
    ("goaws", "stands in for SNS and SQS locally"),
    ("Distroless, non-root images", "no shell, no package manager"),
    ("Separate admin port", "health probes and Prometheus metrics"),
    ("Fixed shutdown order", "the outbox relay stops last"),
]
PLANNED = [
    ("AWS EKS", "Kubernetes in the cloud"),
    ("Terraform", "the real topic, queues and redrive policies"),
    ("Helm", "packaging the nine workloads"),
    ("GitHub Actions", "build, scan and deploy the images"),
    ("Queue-depth autoscaling", "scale consumers on waiting messages"),
]


def scene_platform(ctx, t, D):
    header(ctx, 10, "WHERE IT RUNS")
    for c, (title, sub, col, rows, t0) in enumerate([
        ("Today", "on one machine", TEAL, TODAY, 0.3),
        ("Planned", "not in the repository yet", AMBER, PLANNED, 7.0),
    ]):
        x = 80 + c * 900
        a = seg(t, t0, t0 + 0.6)
        box(ctx, x, 130, 860, 690, fill=PANEL, stroke=col, a=a, r=18, dash=None if c == 0 else [12, 8])
        w = text(ctx, title, x + 36, 198, 40, col, a, bold=True)
        text(ctx, sub, x + 52 + w, 198, 24, MUTED, a)
        for i, (name, what) in enumerate(rows):
            ra = seg(t, t0 + 0.8 + i * 0.7, t0 + 1.4 + i * 0.7)
            y = 250 + i * 108
            box(ctx, x + 36, y, 788, 88, fill=PANEL2, stroke=LINE, a=ra, r=12, lw=1.5)
            if c == 0:
                check(ctx, x + 76, y + 44, 24, col, ra)
            else:
                src(ctx, col, ra)
                ctx.set_line_width(2.5)
                ctx.arc(x + 76, y + 44, 11, 0, 2 * math.pi)
                ctx.stroke()
            text(ctx, name, x + 116, y + 40, 25, INK, ra, bold=True)
            text(ctx, what, x + 116, y + 70, 19, MUTED, ra)
    captions(ctx, t, [
        (0.4, "Today the whole platform runs on one computer, started with a single command.",
         "docker compose with goaws emulating SNS and SQS; pinned, distroless, non-root images."),
        (6.8, "Next stop is the cloud, where it can grow when the crowd arrives.",
         "Planned: EKS from Terraform, Helm charts, a GitHub Actions pipeline, scaling on SQS queue depth."),
    ], D)


# --------------------------------------------------------------- 12. end

def scene_end(ctx, t, D):
    ticket(ctx, W / 2 - 60, 150, 120, 78, AMBER, seg(t, 0.2, 0.9), fill=mix(BG, AMBER, 0.08), lw=4)
    text(ctx, "TicketDrop", W / 2, 350, 96, INK, seg(t, 0.3, 1.1), bold=True, align="c")
    text(ctx, "Correct under a crowd, and recoverable when things break.", W / 2, 420, 32, MUTED,
         seg(t, 0.8, 1.6), align="c")
    for i, (title, _) in enumerate(PROMISES):
        a = seg(t, 1.4 + i * 0.35, 2.0 + i * 0.35)
        x = 100 + i * 440
        box(ctx, x, 520, 400, 96, fill=PANEL, stroke=TEAL, a=a, r=16)
        check(ctx, x + 46, 568, 26, TEAL, a, seg(t, 1.7 + i * 0.35, 2.2 + i * 0.35))
        text(ctx, title, x + 84, 578, 25, INK, a, bold=True, maxw=296)
    a = seg(t, 3.2, 4.0)
    text(ctx, "The reasons, the costs and the evidence are in the repository:", W / 2, 730, 24, MUTED, a, align="c")
    text(ctx, "docs/DECISIONS.md   ·   docs/PROOF.md   ·   docs/TROUBLESHOOTING.md", W / 2, 780, 26, BLUE, a,
         bold=True, mono=True, align="c")
    text(ctx, "github.com/kingswanzy2020/ticketdrop", W / 2, 880, 24, DIM, a, mono=True, align="c")


SCENES = [  # (draw, seconds)
    (scene_title, 7.0),
    (scene_problem, 14.0),
    (scene_services, 20.0),
    (scene_events, 21.0),
    (scene_journey, len(JOURNEY) * STEP + 2.0),
    (scene_failure, 17.0),
    (scene_oversell, 18.5),
    (scene_reliable, 21.5),
    (scene_scheduler, 20.0),
    (scene_proof, 22.0),
    (scene_platform, 14.0),
    (scene_end, 8.0),
]

# What the voice says: {scene: [(second within the scene, text)]}. Written to
# be heard, so abbreviations are spelled the way they are spoken.
NARRATION = {
    "scene_title": [
        (0.5, "This is Ticket Drop: a ticketing platform made of nine small Go services, built to stay correct "
              "when a crowd arrives all at once."),
    ],
    "scene_problem": [
        (0.3, "A ticket drop is the hardest moment for a ticketing system. There are far more buyers than "
              "tickets, everyone arrives in the same second, and behind the scenes, services can crash and "
              "restart."),
        (6.6, "So the platform makes four promises. It never oversells. It never loses an order. It never "
              "charges anyone twice. And it never leaves an order stuck."),
    ],
    "scene_services": [
        (0.4, "The work is split between nine services, each with one job. Web is the shop window, and the "
              "gateway is the front door. Catalog is the event listing. Orders is the order desk, inventory is "
              "the stockroom, and payments is the till. Fulfillment prints the tickets, notifications is the "
              "messenger, and the scheduler is the clock."),
        (11.2, "Each service keeps its own Postgres database, and none of them can read another's tables. That "
               "keeps them independent, but it also means nothing can be done in one shared transaction."),
    ],
    "scene_events": [
        (0.4, "So how do they work together? They do not call each other. A service writes an event to its "
              "own outbox table, and a relay publishes it to a single S N S topic, like pinning a note to a "
              "shared noticeboard."),
        (7.4, "Every consuming service has its own S Q S queue, its own mailbox, filtered so that it only "
              "receives the event types it asked for. Here, a new order goes only to payments."),
        (14.0, "One event can reach several services at once. And if a service is down, its messages simply "
               "wait in the queue until it comes back."),
    ],
    "scene_journey": [(i * STEP + 0.2, line_) for i, line_ in enumerate([
        "Let's follow one order. At the opening time, the scheduler tells catalog to open the drop. Catalog "
        "publishes drop opened, and inventory creates the stock.",
        "A customer places an order in the shop. It travels through the gateway, which only forwards routes "
        "that are explicitly listed, to the orders service.",
        "Before accepting, orders calls inventory directly to hold the tickets. If the drop is sold out, the "
        "customer is told straight away.",
        "Orders then saves the order as pending, and publishes order created.",
        "Payments picks that up, charges the customer, and publishes payment succeeded.",
        "Inventory turns the hold into a sale, and publishes order confirmed.",
        "Fulfillment issues the tickets, and publishes ticket issued.",
        "Orders marks the order as ticketed, and announces the result.",
        "Finally, notifications tells the customer, and the shop page updates on its own.",
    ])],
    "scene_failure": [
        (0.4, "Not every order ends well. If the card is declined, payments publishes payment failed. "
              "Inventory puts the tickets back in stock, orders marks the order as failed, and the customer is "
              "told."),
        (8.2, "And if the payment provider is down, no answer comes at all. So every hold has an expiry. The "
              "scheduler prompts inventory to release expired holds, the tickets return to stock, and the "
              "order fails cleanly. Nobody is ever charged for a hold that has expired."),
    ],
    "scene_oversell": [
        (0.4, "Now, the promises. First: never oversell. Imagine two buyers reaching for the last ticket at "
              "the same instant."),
        (5.0, "Only one of them gets it. A hold is a single database statement: subtract the tickets, but "
              "only where enough are still available. The check and the change happen together, so there is "
              "no gap for a second buyer to slip through."),
        (12.0, "In testing, four hundred simultaneous requests for one hundred tickets produced exactly one "
               "hundred holds."),
    ],
    "scene_reliable": [
        (0.2, "Second: never lose an order. A service saves its change, and the event describing it, in one "
              "database transaction. If the process is killed a moment later, the event is still in the "
              "outbox, and the relay publishes it after the restart."),
        (7.0, "That means an event can occasionally arrive twice. So each consumer records the I D of every "
              "event it handles, in the same transaction as its work. A repeat is recognised and changes "
              "nothing, which is why no one is charged twice."),
        (14.0, "And a message that cannot be processed is retried three times, then moved to a dead letter "
               "queue for a person to inspect, so it never blocks the messages behind it."),
    ],
    "scene_scheduler": [
        (0.4, "Third: never leave an order stuck. The scheduler is only a clock. Every second it asks catalog "
              "to open any drops that are due, and every five seconds it asks inventory to expire unpaid "
              "holds."),
        (11.2, "It runs as two replicas, and the one holding a Postgres advisory lock is the leader. If the "
               "leader is killed, the standby takes the lock and carries on."),
    ],
    "scene_proof": [
        (0.4, "None of this is taken on trust. A script sells out a drop, with six hundred orders competing "
              "for one thousand tickets, and kills a service in the middle. In every run, nothing was "
              "oversold, every accepted order finished as ticketed or failed, each customer was told exactly "
              "once, and every replayed event was caught as a duplicate."),
        (10.8, "A second script takes the payment provider down, and kills the scheduler's leader. The standby "
               "took over in one second, all five orders failed cleanly, all twenty tickets returned to stock, "
               "and nobody was charged."),
    ],
    "scene_platform": [
        (0.4, "Today the whole platform runs on one machine with Docker Compose, using an emulator in place "
              "of S N S and S Q S. Every service runs in a minimal container, as a non root user, and exposes "
              "health checks and metrics."),
        (6.8, "The next step is the cloud: Kubernetes on A W S, with the infrastructure defined in Terraform, "
              "and consumers that scale with the length of their queues."),
    ],
    "scene_end": [
        (0.4, "That is Ticket Drop: correct under a crowd, and recoverable when things break. The decisions, "
              "their costs, and the evidence are all in the repository."),
    ],
}
LEAD, TAIL = 0.2, 0.5   # silence before and after each piece of narration


# ---------------------------------------------------------------- timing

def plan(lengths):
    """Lays the scenes out in video time.

    lengths is {(scene index, cue index): seconds of speech}, or None for a
    silent build. Each stretch of a scene between two cues is given at least
    as long as its narration takes, so the pictures slow down for the voice.
    Returns the segments (video start, video end, scene, scene start, scene
    end), when each piece of narration starts, and the total length.
    """
    segments, starts, at = [], {}, 0.0
    for si, (fn, d) in enumerate(SCENES):
        cues = NARRATION.get(fn.__name__, []) if lengths else []
        marks = [0.0] + [c for c, _ in cues] + [d]
        for k in range(len(marks) - 1):
            need = marks[k + 1] - marks[k]
            if k >= 1:
                last = k == len(marks) - 2   # leave the fade to the next scene clear
                need = max(need, LEAD + lengths[(si, k - 1)] + TAIL + (0.4 if last else 0))
                starts[(si, k - 1)] = at + LEAD
            segments.append((at, at + need, si, marks[k], marks[k + 1]))
            at += need
    return segments, starts, at


def speak(voice_path, folder):
    """Speaks every line of narration into its own .wav; returns their lengths and the sample rate."""
    try:
        from piper import PiperVoice
    except ImportError:
        sys.exit("piper not found: pip install piper-tts, or build with --silent")
    voice = PiperVoice.load(voice_path)
    lengths, rate = {}, None
    for si, (fn, _) in enumerate(SCENES):
        for ci, (_, words) in enumerate(NARRATION.get(fn.__name__, [])):
            path = os.path.join(folder, f"say-{si:02d}-{ci:02d}.wav")
            with wave.open(path, "wb") as out:
                voice.synthesize_wav(words, out)
            with wave.open(path) as w:
                assert w.getnchannels() == 1 and w.getsampwidth() == 2, "expected 16-bit mono from piper"
                rate = w.getframerate()
                lengths[(si, ci)] = w.getnframes() / rate
    return lengths, rate


def soundtrack(folder, starts, total, rate):
    """Places every narration .wav at its start time on one track."""
    track = array("h", bytes(2 * int(total * rate)))
    for (si, ci), start in starts.items():
        with wave.open(os.path.join(folder, f"say-{si:02d}-{ci:02d}.wav")) as w:
            clip = array("h", w.readframes(w.getnframes()))
        at = int(start * rate)
        track[at:at + len(clip)] = clip[:len(track) - at]
    path = os.path.join(folder, "narration.wav")
    with wave.open(path, "wb") as out:
        out.setnchannels(1)
        out.setsampwidth(2)
        out.setframerate(rate)
        out.writeframes(track.tobytes())
    return path


# ---------------------------------------------------------------- render

def draw_frame(ctx, T, segments, total):
    g = cairo.LinearGradient(0, 0, 0, H)
    g.add_color_stop_rgb(0, *BG2)
    g.add_color_stop_rgb(1, *BG)
    ctx.set_source(g)
    ctx.paint()
    for v0, v1, si, a, b in segments:
        if T < v1:
            break
    fn, d = SCENES[si]
    t = min(b, a + (T - v0) / (v1 - v0) * (b - a))
    last = si == len(SCENES) - 1
    fade = seg(t, 0, 0.45) * (1 - seg(t, d - 0.4, d) if not last else 1 - seg(t, d - 0.8, d))
    ctx.push_group()
    fn(ctx, t, d)
    ctx.pop_group_to_source()
    ctx.paint_with_alpha(fade)
    src(ctx, LINE)
    ctx.rectangle(0, 0, W, 5)
    ctx.fill()
    src(ctx, AMBER)
    ctx.rectangle(0, 0, W * min(1, T / total), 5)
    ctx.fill()


def find_ffmpeg():
    exe = os.environ.get("FFMPEG") or shutil.which("ffmpeg")
    if exe:
        return exe
    try:
        import imageio_ffmpeg
        return imageio_ffmpeg.get_ffmpeg_exe()
    except ImportError:
        sys.exit("ffmpeg not found: install it, set $FFMPEG, or pip install imageio-ffmpeg")


def render_part(args):
    exe, first, last, path, segments, total = args
    surface = cairo.ImageSurface(cairo.FORMAT_RGB24, W, H)
    ctx = cairo.Context(surface)
    proc = subprocess.Popen(
        [exe, "-loglevel", "error", "-y", "-f", "rawvideo", "-pix_fmt", "bgr0", "-s", f"{W}x{H}", "-r", str(FPS),
         "-i", "-", "-c:v", "libx264", "-preset", "slow", "-crf", "20", "-pix_fmt", "yuv420p", path],
        stdin=subprocess.PIPE)
    for f in range(first, last):
        draw_frame(ctx, f / FPS, segments, total)
        surface.flush()
        proc.stdin.write(surface.get_data())
    proc.stdin.close()
    if proc.wait() != 0:
        raise RuntimeError(f"ffmpeg failed on frames {first}-{last}")
    return path


def main():
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--out", default=os.path.join(HERE, "ticketdrop-explainer.mp4"))
    ap.add_argument("--jobs", type=int, default=os.cpu_count() or 4)
    ap.add_argument("--voice", default=os.environ.get("PIPER_VOICE"), help="the Piper voice, an .onnx file")
    ap.add_argument("--silent", action="store_true", help="build without narration")
    ap.add_argument("--still", nargs=2, metavar=("SECOND", "PNG"), help="write one frame and exit")
    a = ap.parse_args()

    with tempfile.TemporaryDirectory() as tmp:
        if a.silent:
            lengths = None
        elif a.voice:
            lengths, rate = speak(a.voice, tmp)
        elif a.still:   # near enough to check a layout without a voice installed
            lengths = {(si, ci): len(words.split()) / 2.7 for si, (fn, _) in enumerate(SCENES)
                       for ci, (_, words) in enumerate(NARRATION.get(fn.__name__, []))}
        else:
            sys.exit("name a Piper voice with --voice or $PIPER_VOICE, or build with --silent")
        segments, starts, total = plan(lengths)

        if a.still:
            surface = cairo.ImageSurface(cairo.FORMAT_RGB24, W, H)
            draw_frame(cairo.Context(surface), float(a.still[0]), segments, total)
            surface.write_to_png(a.still[1])
            return

        exe = find_ffmpeg()
        frames = round(total * FPS)
        per = math.ceil(frames / a.jobs)
        parts = [(exe, i, min(i + per, frames), os.path.join(tmp, f"part{n:03d}.mp4"), segments, total)
                 for n, i in enumerate(range(0, frames, per))]
        with ProcessPoolExecutor(a.jobs) as pool:
            done = list(pool.map(render_part, parts))
        listing = os.path.join(tmp, "parts.txt")
        with open(listing, "w") as f:
            f.writelines(f"file '{p}'\n" for p in done)
        cmd = [exe, "-loglevel", "error", "-y", "-f", "concat", "-safe", "0", "-i", listing]
        if lengths:
            cmd += ["-i", soundtrack(tmp, starts, total, rate), "-c:a", "aac", "-b:a", "128k"]
        subprocess.run(cmd + ["-c:v", "copy", "-movflags", "+faststart", a.out], check=True)
    print(f"{a.out}: {int(total // 60)} min {total % 60:.0f} s, {frames} frames,",
          "narrated" if lengths else "silent")


if __name__ == "__main__":
    main()
