#!/usr/bin/env python3
"""Builds the project's architecture diagrams.

Each diagram is written twice: as an .excalidraw file, to open and edit in
Excalidraw, and as an .svg, which the guide pages embed.

    python3 docs/diagrams/build.py

Colours mean the same thing in every diagram:
    blue    our Go services          orange  messaging (SNS, SQS)
    green   data stores              violet  cluster controllers
    cyan    network edge             teal    Git and CI
    gray    people                   red     failure paths
    yellow  a state to notice
"""
import html
import json
import os
import sys

sys.path.insert(0, os.path.expanduser("~/.claude/skills/excalidraw-diagrams/scripts"))
from excalidraw_generator import BoxStyle, Diagram, DiagramStyle  # noqa: E402

HERE = os.path.dirname(os.path.abspath(__file__))


def new():
    return Diagram(
        diagram_style=DiagramStyle(roughness=0, stroke_width=2),
        box_style=BoxStyle(font_family="normal", font_size=16),
    )


def zone(d, x, y, w, h, title, color="gray"):
    """A dashed region that groups the boxes drawn on top of it."""
    el = d.box(x, y, "", width=w, height=h, color=color)
    d.elements.pop()  # the empty label
    el._data["boundElements"] = []
    el._data["backgroundColor"] = "transparent"
    el._data["strokeStyle"] = "dashed"
    el._data["strokeWidth"] = 1
    if title:
        d.text_box(x + 12, y + 8, title, font_size=14, color=color)
    return el


# --------------------------------------------------------------------------
# 1. The journey of one order (what is built today)
# --------------------------------------------------------------------------
def order_journey():
    d = new()
    # Before any order: the drop opens on its own, and inventory gets its tickets.
    scheduler = d.box(250, -70, "scheduler\nkeeps the time", width=180, height=60, color="blue")
    catalog = d.box(540, -70, "catalog\nopens the drop", width=190, height=60, color="blue")

    customer = d.box(-150, 90, "Customer", width=120, height=60, color="gray", shape="ellipse")
    web = d.box(30, 90, "web\nthe shopfront", width=150, height=60, color="blue")
    gateway = d.box(250, 90, "gateway\nlisted routes only", width=180, height=60, color="cyan")
    orders = d.box(540, 90, "orders\naccepts the order", width=190, height=60, color="blue")
    hold = d.box(880, 90, "inventory\nholds the tickets", width=190, height=60, color="blue")
    d.arrow_between(scheduler, catalog, "it is time (HTTP)", from_side="right", to_side="left")
    d.arrow_between(catalog, hold, "drop.opened", from_side="right", to_side="top")
    d.arrow_between(customer, web, "buys", from_side="right", to_side="left")
    d.arrow_between(web, gateway, "API", from_side="right", to_side="left")
    d.arrow_between(gateway, orders, "forwards", from_side="right", to_side="left")
    d.arrow_between(orders, hold, "hold (HTTP)", from_side="right", to_side="left")

    zone(d, 40, 250, 1100, 170, "", color="orange")
    d.text_box(620, 222, "Each arrow in this band is an event: outbox → SNS topic → the consumer's SQS queue",
               font_size=13, color="orange")
    payments = d.box(70, 320, "payments\ncharges the card", width=190, height=64, color="blue")
    confirm = d.box(350, 320, "inventory\nconfirms the hold", width=190, height=64, color="blue")
    fulfil = d.box(630, 320, "fulfillment\nissues the tickets", width=190, height=64, color="blue")
    done = d.box(910, 320, "orders\nmarks it ticketed", width=200, height=64, color="green")
    d.arrow_between(orders, payments, "order.created", from_side="bottom", to_side="top")
    d.arrow_between(payments, confirm, "payment.\nsucceeded", from_side="right", to_side="left")
    d.arrow_between(confirm, fulfil, "order.\nconfirmed", from_side="right", to_side="left")
    d.arrow_between(fulfil, done, "ticket.\nissued", from_side="right", to_side="left")

    failed = d.box(-70, 520, "Card declined:\ninventory releases the tickets,\norders marks the order failed",
                   width=320, height=84, color="red")
    d.arrow_between(payments, failed, "payment.failed", color="red", from_side="bottom", to_side="top")
    expired = d.box(285, 520, "No payment answer in 10 minutes:\ninventory releases the tickets,\norders marks the order failed",
                    width=320, height=84, color="red")
    d.arrow_between(confirm, expired, "hold.expired", color="red", from_side="bottom", to_side="top")
    dlq = d.box(640, 520, "A message that fails 3 times\nmoves to that queue's\ndead-letter queue",
                width=320, height=84, color="yellow")
    d.arrow_between(fulfil, dlq, "cannot be handled", color="red", from_side="bottom", to_side="top")
    told = d.box(1000, 520, "notifications\ntells the customer\nhow it ended", width=200, height=84, color="blue")
    d.arrow_between(done, told, "order.ticketed\nor order.failed", from_side="bottom", to_side="top")
    return d


# --------------------------------------------------------------------------
# 2. Phase 2: the same system on Docker Desktop's Kubernetes
# --------------------------------------------------------------------------
def phase2_cluster():
    d = new()
    you = d.box(-80, 285, "You\ncurl / drop-check", width=150, height=70, color="gray", shape="ellipse")
    zone(d, 200, 40, 1000, 590, "Docker Desktop Kubernetes  (context: docker-desktop)", color="black")

    zone(d, 220, 90, 180, 510, "namespace: traefik", color="cyan")
    traefik = d.box(235, 285, "Traefik\nGateway +\nHTTPRoute", width=150, height=80, color="cyan")

    zone(d, 430, 90, 450, 510, "namespace: ticketdrop", color="blue")
    gw = d.box(480, 295, "gateway", width=110, height=56, color="blue")
    web = d.box(465, 150, "web", width=110, height=56, color="blue")
    orders = d.box(620, 150, "orders", width=110, height=56, color="blue")
    inv = d.box(750, 150, "inventory", width=110, height=56, color="blue")
    d.box(750, 222, "notifications", width=110, height=56, color="blue")
    pay = d.box(620, 295, "payments", width=110, height=56, color="blue")
    ful = d.box(750, 295, "fulfillment\n0 → N pods", width=110, height=56, color="blue")
    d.box(620, 375, "catalog", width=110, height=56, color="blue")
    d.box(465, 375, "scheduler\n1 of 2 leads", width=140, height=56, color="blue")
    d.text_box(460, 450, "Every pod:  probes and metrics on :9090,\nnon-root,  read-only filesystem.\nThe five HTTP services also serve on :8080.", font_size=14, color="blue")

    zone(d, 940, 90, 240, 290, "namespace: data", color="green")
    pg = d.box(960, 140, "PostgreSQL\n(CloudNativePG)\none DB per service", width=200, height=84, color="green")
    bus = d.box(960, 270, "goaws\nSNS + SQS emulator", width=200, height=64, color="orange")

    zone(d, 940, 440, 240, 160, "namespace: keda", color="violet")
    keda = d.box(960, 510, "KEDA", width=200, height=56, color="violet")

    d.arrow_between(you, traefik, "http://localhost", from_side="right", to_side="left")
    d.arrow_between(traefik, gw, "/v1/*", from_side="right", to_side="left")
    d.arrow_between(traefik, web, "pages", from_side="top", to_side="left")
    d.arrow_between(web, gw, "API", from_side="bottom", to_side="top")
    d.arrow_between(gw, orders, "routes", from_side="right", to_side="left")
    d.arrow_between(inv, pg, "SQL", from_side="right", to_side="left")
    d.arrow_between(ful, bus, "events", from_side="right", to_side="left")
    d.arrow_between(keda, bus, "queue depth", color="violet", from_side="top", to_side="bottom")
    d.arrow_between(keda, ful, "scales", color="violet", from_side="left", to_side="bottom")
    return d


# --------------------------------------------------------------------------
# 3. Where this is heading: the platform on AWS
# --------------------------------------------------------------------------
def aws_target():
    d = new()
    user = d.box(30, 40, "Customer", width=130, height=60, color="gray", shape="ellipse")
    edge = d.box(250, 40, "Route 53  →  Network Load Balancer", width=330, height=60, color="cyan")
    d.arrow_between(user, edge, "HTTPS", from_side="right", to_side="left")

    zone(d, 20, 150, 830, 500, "Amazon EKS  ·  private subnets across 3 availability zones", color="black")
    traefik = d.box(50, 210, "Traefik\nTLS + Gateway API", width=190, height=70, color="cyan")
    svc = d.box(330, 210, "9 Go services\nHPA and KEDA", width=190, height=70, color="blue")
    data = d.box(610, 210, "PostgreSQL + Valkey\non EBS volumes", width=210, height=70, color="green")
    d.arrow_between(edge, traefik, from_side="bottom", to_side="top")
    d.arrow_between(traefik, svc, from_side="right", to_side="left")
    d.arrow_between(svc, data, from_side="right", to_side="left")

    d.text_box(50, 330, "Controllers that run the cluster for you", font_size=14, color="violet")
    karp = d.box(50, 360, "Karpenter\nadds and removes\nSpot nodes", width=180, height=84, color="violet")
    keda = d.box(250, 360, "KEDA\nscales consumers\non queue depth", width=180, height=84, color="violet")
    dns = d.box(450, 360, "cert-manager +\nexternal-dns\nTLS and DNS", width=180, height=84, color="violet")
    csi = d.box(650, 360, "Secrets Store\nCSI driver", width=150, height=84, color="violet")

    argo = d.box(50, 520, "Argo CD\nmakes the cluster match Git", width=260, height=70, color="violet")
    obs = d.box(380, 520, "Prometheus + Grafana\nmetrics, dashboards, alerts", width=260, height=70, color="teal")

    bus = d.box(920, 40, "SNS topic +\nSQS queues and\ndead-letter queues", width=210, height=90, color="orange")
    s3 = d.box(920, 200, "S3\nissued tickets", width=210, height=60, color="green")
    sm = d.box(920, 374, "Secrets Manager", width=210, height=56, color="yellow")
    ecr = d.box(920, 520, "ECR\ncontainer images", width=210, height=70, color="teal")
    d.arrow_between(svc, bus, "events", from_side="top", to_side="left")
    d.arrow_between(csi, sm, "reads", from_side="right", to_side="left")

    repo = d.box(50, 720, "GitOps repo\nthe cluster's desired state", width=260, height=70, color="teal")
    ci = d.box(520, 720, "GitHub Actions\ntest · scan · build · sign", width=260, height=70, color="teal")
    d.arrow_between(argo, repo, "pulls", from_side="bottom", to_side="top")
    d.arrow_between(ci, repo, "new image tag", from_side="left", to_side="right")
    d.arrow_between(ci, ecr, "pushes image", from_side="right", to_side="bottom")
    return d


# --------------------------------------------------------------------------
# 4. How a change reaches the cluster
# --------------------------------------------------------------------------
def delivery():
    d = new()
    you = d.box(30, 80, "You", width=100, height=60, color="gray", shape="ellipse")
    pr = d.box(210, 80, "Pull request\nto the app repo", width=170, height=60, color="teal")
    ci = d.box(470, 80, "CI checks\nlint · test · scan", width=180, height=60, color="teal")
    merge = d.box(740, 80, "Merge to main", width=160, height=60, color="teal")
    build = d.box(990, 80, "Build image,\npush to ECR", width=160, height=60, color="teal")
    d.arrow_between(you, pr, "push", from_side="right", to_side="left")
    d.arrow_between(pr, ci, from_side="right", to_side="left")
    d.arrow_between(ci, merge, "green", from_side="right", to_side="left")
    d.arrow_between(merge, build, from_side="right", to_side="left")

    bump = d.box(960, 290, "Commit the new\nimage tag to the\nGitOps repo", width=190, height=84, color="teal")
    argo = d.box(660, 290, "Argo CD sees\nthe commit", width=180, height=84, color="violet")
    roll = d.box(360, 290, "Pods roll to the\nnew image, a few\nat a time", width=180, height=84, color="blue")
    ok = d.box(40, 290, "Readiness passes,\nor the rollout\nstops", width=200, height=84, color="green")
    d.arrow_between(build, bump, "tagged with the commit SHA", from_side="bottom", to_side="top")
    d.arrow_between(bump, argo, from_side="left", to_side="right")
    d.arrow_between(argo, roll, "sync", from_side="left", to_side="right")
    d.arrow_between(roll, ok, from_side="left", to_side="right")
    d.text_box(40, 430,
               "The pipeline never talks to the cluster. Argo CD, running inside it, pulls from Git.\n"
               "So no cluster credential ever lives in GitHub.", font_size=15, color="black")
    return d


# --------------------------------------------------------------------------
# 5. The scaling loop a drop sets off
# --------------------------------------------------------------------------
def scaling_loop():
    d = new()
    drop = d.box(40, 80, "A drop opens:\norders flood in", width=200, height=70, color="gray")
    backlog = d.box(370, 80, "The fulfillment\nqueue backs up", width=200, height=70, color="orange")
    keda = d.box(700, 80, "KEDA adds\nfulfillment pods", width=200, height=70, color="violet")
    pending = d.box(1030, 80, "New pods are Pending:\nthe nodes are full", width=210, height=70, color="yellow")
    d.arrow_between(drop, backlog, from_side="right", to_side="left")
    d.arrow_between(backlog, keda, "over target", from_side="right", to_side="left")
    d.arrow_between(keda, pending, from_side="right", to_side="left")

    karp = d.box(1030, 270, "Karpenter launches\nSpot nodes", width=210, height=70, color="violet")
    drain = d.box(700, 270, "Pods start,\nthe backlog drains", width=200, height=70, color="blue")
    zero = d.box(370, 270, "Queue empty: KEDA\nscales back to zero", width=200, height=70, color="violet")
    gone = d.box(40, 270, "Karpenter removes\nthe empty nodes", width=200, height=70, color="green")
    d.arrow_between(pending, karp, from_side="bottom", to_side="top")
    d.arrow_between(karp, drain, "~1 minute", from_side="left", to_side="right")
    d.arrow_between(drain, zero, from_side="left", to_side="right")
    d.arrow_between(zero, gone, from_side="left", to_side="right")
    return d


# --------------------------------------------------------------------------
# Excalidraw JSON -> SVG
# --------------------------------------------------------------------------
FONT = "ui-sans-serif, system-ui, 'Segoe UI', Roboto, Helvetica, Arial, sans-serif"


def to_svg(elements, title):
    xs, ys, xe, ye = [], [], [], []
    for e in elements:
        if e["type"] in ("arrow", "line"):
            for px, py in e["points"]:
                xs.append(e["x"] + px); xe.append(e["x"] + px)
                ys.append(e["y"] + py); ye.append(e["y"] + py)
        else:
            xs.append(e["x"]); ys.append(e["y"])
            xe.append(e["x"] + e["width"]); ye.append(e["y"] + e["height"])
    pad = 24
    x0, y0 = min(xs) - pad, min(ys) - pad
    w, h = max(xe) - x0 + pad, max(ye) - y0 + pad

    out = [
        f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="{x0:.0f} {y0:.0f} {w:.0f} {h:.0f}" '
        f'role="img" aria-label="{html.escape(title)}" font-family="{FONT}">',
        f'<rect x="{x0:.0f}" y="{y0:.0f}" width="{w:.0f}" height="{h:.0f}" fill="#ffffff"/>',
    ]
    markers = {}
    body = []
    for e in elements:
        stroke, fill = e["strokeColor"], e.get("backgroundColor", "transparent")
        fill = "none" if fill == "transparent" else fill
        dash = ' stroke-dasharray="7 6"' if e.get("strokeStyle") == "dashed" else ""
        sw = e.get("strokeWidth", 2)
        t = e["type"]
        if t == "rectangle":
            body.append(f'<rect x="{e["x"]:.1f}" y="{e["y"]:.1f}" width="{e["width"]:.1f}" height="{e["height"]:.1f}" '
                        f'rx="10" fill="{fill}" stroke="{stroke}" stroke-width="{sw}"{dash}/>')
        elif t == "ellipse":
            body.append(f'<ellipse cx="{e["x"] + e["width"] / 2:.1f}" cy="{e["y"] + e["height"] / 2:.1f}" '
                        f'rx="{e["width"] / 2:.1f}" ry="{e["height"] / 2:.1f}" fill="{fill}" stroke="{stroke}" stroke-width="{sw}"/>')
        elif t in ("arrow", "line"):
            pts = " ".join(f'{e["x"] + px:.1f},{e["y"] + py:.1f}' for px, py in e["points"])
            mid = "m" + stroke.lstrip("#")
            markers[mid] = stroke
            end = f' marker-end="url(#{mid})"' if t == "arrow" else ""
            body.append(f'<polyline points="{pts}" fill="none" stroke="{stroke}" stroke-width="{sw}"{end}/>')
        elif t == "text":
            lines = e["text"].split("\n")
            size = e["fontSize"]
            lh = size * 1.25
            centred = e.get("textAlign") == "center"
            tx = e["x"] + e["width"] / 2 if centred else e["x"]
            anchor = "middle" if centred else "start"
            if fill != "none":  # an arrow label: clear the line behind it
                longest = max(len(line) for line in lines) * size * 0.56 + 10
                body.append(f'<rect x="{tx - longest / 2:.1f}" y="{e["y"] - 2:.1f}" width="{longest:.1f}" '
                            f'height="{lh * len(lines) + 4:.1f}" rx="4" fill="#ffffff"/>')
            weight = ' font-weight="600"' if e.get("containerId") and fill == "none" else ""
            spans = "".join(
                f'<tspan x="{tx:.1f}" y="{e["y"] + lh * i + size:.1f}">{html.escape(line)}</tspan>'
                for i, line in enumerate(lines))
            body.append(f'<text font-size="{size}" text-anchor="{anchor}" fill="{stroke}"{weight}>{spans}</text>')
    out.append("<defs>")
    for mid, colour in markers.items():
        out.append(f'<marker id="{mid}" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" '
                   f'orient="auto-start-reverse"><path d="M0 0L10 5L0 10z" fill="{colour}"/></marker>')
    out.append("</defs>")
    # Shapes first, then connectors, then text, so labels are never covered.
    order = {"<rect": 0, "<elli": 0, "<poly": 1, "<text": 2}
    labels = [b for b in body if b.startswith("<rect") and 'rx="4"' in b]
    rest = [b for b in body if b not in labels]
    rest.sort(key=lambda b: order[b[:5]])
    texts = [b for b in rest if b.startswith("<text")]
    out += [b for b in rest if not b.startswith("<text")] + labels + texts
    out.append("</svg>")
    return "\n".join(out)


DIAGRAMS = {
    "order-journey": ("The journey of one order through the services", order_journey),
    "phase2-cluster": ("The system on Docker Desktop Kubernetes in Phase 2", phase2_cluster),
    "aws-target": ("The finished platform on AWS", aws_target),
    "delivery": ("How a code change reaches the cluster", delivery),
    "scaling-loop": ("The scaling loop a ticket drop sets off", scaling_loop),
}

if __name__ == "__main__":
    for name, (title, build) in DIAGRAMS.items():
        d = build()
        path = os.path.join(HERE, name)
        d.save(path)
        with open(path + ".excalidraw") as f:
            elements = json.load(f)["elements"]
        with open(path + ".svg", "w") as f:
            f.write(to_svg(elements, title))
        print(f"{name}: {len(elements)} elements")
