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
    d = Diagram.house()
    d.title("The journey of one order",
            "web  ->  gateway  ->  orders  ->  inventory hold   |   then events:  payments  ->  inventory  ->  fulfillment  ->  orders  ->  notifications")

    d.zone(40, 110, 800, 140, "Before any order: the drop opens by itself", "gray")
    scheduler = d.card(60, 155, "scheduler", "keeps the time  -  2 pods, 1 leads", icon="go", color="gray", width=320)
    catalog = d.card(500, 155, "catalog", "opens the drop on time", icon="go", color="gray", width=320)

    d.zone(40, 280, 1640, 150, "While the customer waits: plain HTTP calls", "blue")
    customer = d.card(60, 325, "Customer", "presses Buy", color="gray", width=170)
    web = d.card(310, 325, "web", "the shopfront", icon="go", color="blue", width=230, port=":8080")
    gateway = d.card(620, 325, "gateway", "listed routes only", icon="go", color="blue", width=260, port=":8080")
    orders = d.card(970, 325, "orders", "accepts the order", icon="go", color="blue", width=260, port=":8080")
    hold = d.card(1330, 325, "inventory", "holds the tickets", icon="go", color="blue", width=330, port=":8080")

    d.zone(40, 460, 1640, 350,
           "After that nobody waits: every arrow here is an event   (outbox  ->  SNS topic  ->  the consumer's own SQS queue)",
           "orange")
    payments = d.card(970, 510, "payments", "charges the card", icon="go", color="blue", width=260)
    confirm = d.card(520, 510, "inventory", "confirms the hold", icon="go", color="blue", width=260)
    fulfil = d.card(60, 510, "fulfillment", "issues the tickets", icon="go", color="teal", width=270)
    done = d.card(60, 690, "orders", "marks it ticketed", icon="go", color="green", width=270)
    told = d.card(650, 690, "notifications", "tells the customer how it ended", icon="go", color="blue", width=330)
    declined = d.note(1340, 505, "Card declined:\ninventory releases the tickets,\norders marks the order failed", color="red", width=320)
    expired = d.note(1010, 690, "No payment answer in 10 minutes:\ninventory releases the tickets,\norders marks the order failed", color="red", width=310)
    d.note(1350, 690, "A message that fails 3 times\nmoves to its queue's\ndead-letter queue", color="yellow", width=310)

    d.flow(scheduler, catalog, "it is time", "black", from_side="right", to_side="left")
    d.flow(catalog, hold, "drop.opened", "orange", from_side="right", to_side="top")
    d.flow(customer, web, "buys", "black", step=1, from_side="right", to_side="left")
    d.flow(web, gateway, "API", "black", step=2, from_side="right", to_side="left")
    d.flow(gateway, orders, "forwards", "black", step=3, from_side="right", to_side="left")
    d.flow(orders, hold, "hold", "black", step=4, from_side="right", to_side="left")
    d.flow(orders, payments, "order.created", "orange", step=5, from_side="bottom", to_side="top")
    d.flow(payments, confirm, "payment.succeeded", "orange", step=6, from_side="left", to_side="right")
    d.flow(confirm, fulfil, "order.confirmed", "orange", step=7, from_side="left", to_side="right")
    d.flow(fulfil, done, "ticket.issued", "orange", step=8, from_side="bottom", to_side="top")
    d.flow(done, told, "order.ticketed / failed", "orange", step=9, from_side="right", to_side="left")
    d.flow(payments, declined, "payment.failed", "red", from_side="right", to_side="left")
    d.flow(confirm, expired, "hold.expired", "red", from_side="bottom", to_side="top")

    d.legend(1000, 105, [
        ("HTTP call: someone is waiting", "black", False),
        ("Event: nobody is waiting", "orange", False),
        ("Failure path", "red", False),
    ])
    return d


# --------------------------------------------------------------------------
# 2. Phase 2: the same system on Docker Desktop's Kubernetes (house style)
# --------------------------------------------------------------------------
def phase2_cluster():
    d = Diagram.house()
    d.title("TicketDrop on Docker Desktop Kubernetes",
            "Phase 2   |   Helm chart  ->  Traefik Gateway  ->  9 Go services  ->  KEDA scaling on queue depth")

    d.note(40, 120, "You write every manifest in\nticketdrop-gitops/ and install it\nwith helm. No AWS yet.", width=330)

    d.zone(640, 110, 760, 190, "", "gray")
    d.icon(660, 128, "github", size=40)
    d.text(712, 126, "ticketdrop-gitops", size=22)
    d.text(712, 156, "your repo  -  everything Kubernetes lives here", size=13, hex_color="#495057")
    chart = d.card(660, 200, "charts/service/", "one chart, used by every service", icon="helm", color="gray", width=350)
    d.card(1030, 200, "apps/<service>/values.yaml", "ports, env, replicas, probes", color="gray", width=350)

    d.zone(20, 340, 1900, 860, "Local Developer Machine  (host OS)", "gray", fill=False)

    d.zone(50, 400, 340, 770, "Developer Space", "gray")
    term = d.card(70, 450, "Terminal", "helm install, kubectl get,\nkubectl describe", icon="gnubash", color="gray", width=300, height=92)
    check = d.card(70, 570, "curl / drop-check.sh", "places orders through\nthe front door", color="yellow", width=300, height=92)
    d.note(70, 690, "localhost:<port>  ->  Traefik Gateway\n(8080 is taken on this machine)", width=300)
    d.card(70, 780, "Docker Desktop", "runs the single-node cluster\ncontext: docker-desktop", icon="docker", color="blue", width=300, height=92)
    d.note(70, 900, "Images are built locally and\nused straight from Docker's\nimage store. No registry yet.", width=300, color="gray")

    cluster = d.zone(420, 400, 1470, 770, "", "blue", fill=False)
    k8s = d.icon(444, 414, "kubernetes", size=40)
    d.text(496, 416, "Docker Desktop Cluster   -   context: docker-desktop", size=20, color="blue")
    d.text(496, 444, "single node  -  the same manifests later run on Amazon EKS", size=13, hex_color="#495057")

    d.zone(450, 490, 300, 370, "namespace:  traefik", "cyan")
    d.icon(470, 530, "traefikproxy", size=46)
    d.text(528, 534, "Traefik", size=20)
    d.text(528, 560, "the front door", size=13, hex_color="#495057")
    gateway_obj = d.card(470, 600, "Gateway", "listens on localhost", color="cyan", width=260, port=":<port>")
    route_api = d.card(470, 700, "HTTPRoute  /v1", color="cyan", width=260)
    route_web = d.card(470, 780, "HTTPRoute  /", color="cyan", width=260)

    d.zone(780, 490, 620, 450, "namespace:  ticketdrop   -   one Helm chart, nine releases", "blue")
    col = [790, 994, 1198]
    row = [540, 624, 708]
    svc = {}
    grid = [
        ("gateway", "listed routes only", "blue"), ("catalog", "drops and tiers", "blue"), ("orders", "accepts orders", "blue"),
        ("web", "the shopfront", "blue"), ("inventory", "holds tickets", "blue"), ("payments", "charges cards", "blue"),
        ("scheduler", "2 pods, 1 leads", "blue"), ("notifications", "tells customers", "blue"), ("fulfillment", "0 to N pods", "teal"),
    ]
    for i, (name, what, colour) in enumerate(grid):
        svc[name] = d.card(col[i % 3], row[i // 3], name, what, icon="go", color=colour, width=196, height=70)
    d.text(790, 800, "Every pod:  probes and metrics on :9090  -  non-root, read-only filesystem\n"
           "web, gateway, catalog, orders and inventory also serve on :8080\n"
           "Pod Security: restricted  -  NetworkPolicy: deny by default", size=13, color="blue")

    d.zone(1430, 490, 430, 300, "namespace:  data", "green")
    pg = d.card(1450, 540, "PostgreSQL", "CloudNativePG operator\none database per service", icon="postgresql", color="green", width=390, height=96)
    bus = d.card(1450, 660, "goaws", "SNS + SQS emulator\n5 queues + 5 dead-letter queues", color="orange", width=390, height=96, port=":4100")

    d.zone(1430, 820, 430, 160, "namespace:  keda", "violet")
    keda = d.card(1450, 870, "KEDA", "ScaledObject: fulfillment", color="violet", width=390)

    d.flow(term, k8s, "helm install", "green", step=1, from_side="right", to_side="left")
    d.flow(check, gateway_obj, "HTTP", "black", step=2, from_side="right", to_side="left")
    d.flow(gateway_obj, route_api, color="black", from_side="bottom", to_side="top")
    d.flow(route_api, svc["gateway"], "API", "black", step=3, from_side="right", to_side="left")
    d.flow(route_web, svc["web"], "pages", "black", from_side="right", to_side="left")
    d.flow(svc["web"], svc["gateway"], color="black", from_side="top", to_side="bottom")
    d.flow(svc["gateway"], svc["catalog"], color="black", from_side="right", to_side="left")
    d.flow(svc["orders"], pg, "SQL", "green", from_side="right", to_side="left")
    d.flow(svc["payments"], bus, "events", "orange", step=4, from_side="right", to_side="left")
    d.flow(keda, bus, "queue depth", "violet", dashed=True, step=5, from_side="top", to_side="bottom")
    d.flow(keda, svc["fulfillment"], "scales", "violet", step=6, from_side="left", to_side="right")

    d.legend(450, 990, [
        ("Customer request", "black", False),
        ("helm install / SQL", "green", False),
        ("Events through SNS and SQS", "orange", False),
        ("KEDA watching and scaling", "violet", True),
    ])
    return d


# --------------------------------------------------------------------------
# 3. Where this is heading: the platform on AWS
# --------------------------------------------------------------------------
def aws_target():
    d = Diagram.house()
    d.title("TicketDrop on Amazon EKS",
            "Route 53  ->  NLB  ->  Traefik  ->  9 Go services  ->  SNS + SQS   |   scaled by KEDA and Karpenter   |   delivered by Argo CD")

    customer = d.card(40, 120, "Customers", color="gray", width=180)
    r53 = d.card(330, 120, "Route 53", "records written by external-dns", icon="amazonroute53", color="violet", width=330)
    d.card(760, 120, "Let's Encrypt", "certificates, proven by DNS-01", icon="letsencrypt", color="gray", width=330)

    d.zone(20, 230, 1880, 770, "", "black", fill=False)
    d.icon(40, 244, "amazonwebservices", size=34)
    d.text(86, 248, "AWS Cloud   -   us-east-1", size=18)

    d.zone(50, 300, 1280, 680, "VPC   -   public and private subnets across 3 availability zones", "violet", fill=False)
    nlb = d.card(80, 345, "Network Load Balancer", "public subnets", color="cyan", width=300)
    d.card(430, 345, "NAT gateway", "one, to save cost", color="cyan", width=250)

    d.zone(80, 445, 1220, 510, "", "orange", fill=False)
    d.icon(100, 458, "amazoneks", size=32)
    d.text(142, 462, "Amazon EKS cluster   -   private subnets   -   system nodes on-demand, workload nodes Spot", size=16, color="orange")

    d.zone(110, 505, 290, 150, "namespace:  traefik", "cyan")
    traefik = d.card(125, 550, "Traefik", "TLS + Gateway API", icon="traefikproxy", color="cyan", width=260)
    d.zone(420, 505, 430, 150, "namespace:  ticketdrop", "blue")
    svc = d.card(435, 550, "9 Go services", "HPA on HTTP, KEDA on consumers", icon="go", color="blue", width=400)
    d.zone(870, 505, 400, 150, "namespace:  data", "green")
    pg = d.card(885, 550, "PostgreSQL", "CloudNativePG, EBS volumes", icon="postgresql", color="green", width=370)

    d.zone(110, 680, 1160, 250, "Platform controllers   -   every one installed by Argo CD from the GitOps repo", "violet")
    d.card(125, 725, "external-dns", "writes DNS records", color="violet", width=270)
    d.card(410, 725, "Karpenter", "adds and removes Spot nodes", color="violet", width=270)
    d.card(695, 725, "KEDA", "scales on SQS queue depth", color="violet", width=270)
    d.card(980, 725, "cert-manager", "TLS certificates", color="violet", width=270)
    argo = d.card(125, 825, "Argo CD", "cluster matches Git", icon="argo", color="violet", width=270)
    d.card(410, 825, "Secrets Store CSI", "mounts AWS secrets", color="violet", width=270)
    d.card(695, 825, "Prometheus", "metrics and alerts", icon="prometheus", color="violet", width=270, port=":9090")
    d.card(980, 825, "Grafana", "dashboards", icon="grafana", color="violet", width=270, port=":3000")

    bus = d.card(1380, 330, "SNS topic + SQS queues", "one queue per consumer,\neach with a dead-letter queue", icon="amazonsqs", color="orange", width=490, height=96)
    d.card(1380, 460, "Secrets Manager", "database credentials", icon="awssecretsmanager", color="red", width=490)
    d.card(1380, 570, "ECR", "signed images, tagged by commit SHA", color="orange", width=490)
    d.card(1380, 680, "IAM + EKS Pod Identity", "one role per service account, no stored keys", color="red", width=490)

    gitops = d.card(60, 1040, "GitOps repo", "charts + values", icon="github", color="teal", width=300)
    gha = d.card(480, 1040, "GitHub Actions", "test, scan, build, sign, push to ECR", icon="githubactions", color="teal", width=380)
    repo = d.card(990, 1040, "App repo", "services + Terraform", icon="github", color="teal", width=300)
    you = d.card(1400, 1040, "You", color="gray", width=140)
    d.card(1610, 1040, "Terraform", "provisions all of AWS", icon="terraform", color="gray", width=290)

    d.flow(customer, r53, "HTTPS", "black", step=1, from_side="right", to_side="left")
    d.flow(r53, nlb, "alias", "black", step=2, from_side="bottom", to_side="top")
    d.flow(nlb, traefik, color="black", step=3, from_side="bottom", to_side="top")
    d.flow(traefik, svc, color="black", step=4, from_side="right", to_side="left")
    d.flow(svc, pg, "SQL", "black", from_side="right", to_side="left")
    d.flow(svc, bus, "events", "orange", step=5, from_side="top", to_side="left")
    d.flow(you, repo, "git push", "green", from_side="left", to_side="right")
    d.flow(repo, gha, color="green", from_side="left", to_side="right")
    d.flow(gha, gitops, "new image tag", "green", from_side="left", to_side="right")
    d.flow(argo, gitops, "pulls + syncs", "green", from_side="bottom", to_side="top")

    d.legend(1380, 800, [
        ("Customer traffic", "black", False),
        ("Events through SNS and SQS", "orange", False),
        ("Delivery: Git, CI, Argo CD", "green", False),
    ])
    return d


# --------------------------------------------------------------------------
# 4. How a change reaches the cluster
# --------------------------------------------------------------------------
def delivery():
    d = Diagram.house()
    d.title("How a change reaches the cluster",
            "pull request  ->  CI  ->  merge  ->  ECR  ->  GitOps repo  ->  Argo CD  ->  rolling update")

    you = d.card(40, 140, "You", color="gray", width=140)
    pr = d.card(290, 140, "Pull request", "to the app repo", icon="github", color="teal", width=280)
    ci = d.card(660, 140, "CI checks", "lint, test, Trivy scan", icon="githubactions", color="teal", width=300)
    merge = d.card(1060, 140, "Merge to main", "only when CI is green", icon="git", color="teal", width=300)
    build = d.card(1470, 140, "Build + push", "image to ECR,\ntagged with the commit SHA", icon="docker", color="teal", width=350, height=96)

    d.zone(40, 320, 1320, 170, "Inside the cluster", "blue")
    bump = d.card(1470, 365, "GitOps repo", "CI commits the new image tag", icon="github", color="teal", width=350)
    argo = d.card(990, 365, "Argo CD", "sees the commit, syncs", icon="argo", color="violet", width=340)
    roll = d.card(540, 365, "Rolling update", "pods move to the new image", icon="kubernetes", color="blue", width=340)
    ok = d.card(70, 365, "Readiness gate", "passes, or the rollout stops", color="green", width=340)

    d.flow(you, pr, "git push", "green", step=1, from_side="right", to_side="left")
    d.flow(pr, ci, color="green", step=2, from_side="right", to_side="left")
    d.flow(ci, merge, "green", "green", step=3, from_side="right", to_side="left")
    d.flow(merge, build, color="green", step=4, from_side="right", to_side="left")
    d.flow(build, bump, color="green", step=5, from_side="bottom", to_side="top")
    d.flow(argo, bump, "pulls", "violet", step=6, from_side="right", to_side="left")
    d.flow(argo, roll, "sync", "violet", step=7, from_side="left", to_side="right")
    d.flow(roll, ok, color="violet", step=8, from_side="left", to_side="right")

    d.note(40, 530, "The pipeline never talks to the cluster. Argo CD, running inside it, pulls from Git,\nso no cluster credential ever lives in GitHub.", width=760)
    d.legend(1100, 520, [("The pipeline (GitHub)", "green", False), ("Argo CD (in the cluster)", "violet", False)])
    return d


# --------------------------------------------------------------------------
# 5. The scaling loop a drop sets off
# --------------------------------------------------------------------------
def scaling_loop():
    d = Diagram.house()
    d.title("What happens when a drop opens",
            "queue backlog  ->  KEDA adds pods  ->  Karpenter adds nodes  ->  backlog drains  ->  both scale back")

    drop = d.card(40, 140, "A drop opens", "orders flood in", color="gray", width=300)
    backlog = d.card(470, 140, "fulfillment queue", "messages pile up", icon="amazonsqs", color="orange", width=300)
    keda = d.card(900, 140, "KEDA", "adds fulfillment pods", color="violet", width=300)
    pending = d.card(1330, 140, "Pods are Pending", "the nodes are full", icon="kubernetes", color="yellow", width=320)

    karp = d.card(1330, 340, "Karpenter", "launches Spot nodes", color="violet", width=320)
    drain = d.card(900, 340, "Pods start", "the backlog drains", icon="go", color="blue", width=300)
    zero = d.card(470, 340, "KEDA", "queue empty: back to zero", color="violet", width=300)
    gone = d.card(40, 340, "Karpenter", "removes the empty nodes", color="green", width=300)

    d.flow(drop, backlog, color="orange", step=1, from_side="right", to_side="left")
    d.flow(backlog, keda, "over target", "orange", step=2, from_side="right", to_side="left")
    d.flow(keda, pending, color="violet", step=3, from_side="right", to_side="left")
    d.flow(pending, karp, color="violet", step=4, from_side="bottom", to_side="top")
    d.flow(karp, drain, "~1 minute", "violet", step=5, from_side="left", to_side="right")
    d.flow(drain, zero, color="orange", step=6, from_side="left", to_side="right")
    d.flow(zero, gone, color="violet", step=7, from_side="left", to_side="right")

    d.note(40, 470, "KEDA decides how many pods. Karpenter decides how many machines.\nPhase 2 proves the KEDA half on your laptop; Karpenter needs AWS (Phase 5).", width=640)
    d.legend(900, 460, [("Load: orders and queue depth", "orange", False), ("A controller acting", "violet", False)])
    return d


# --------------------------------------------------------------------------
# Excalidraw JSON -> SVG
# --------------------------------------------------------------------------
FONT = "ui-sans-serif, system-ui, 'Segoe UI', Roboto, Helvetica, Arial, sans-serif"


def to_svg(elements, title, files=None, background="#ffffff"):
    files = files or {}
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
        f'<rect x="{x0:.0f}" y="{y0:.0f}" width="{w:.0f}" height="{h:.0f}" fill="{background}"/>',
    ]
    markers = {}
    body = []
    for e in elements:
        stroke, fill = e["strokeColor"], e.get("backgroundColor", "transparent")
        fill = "none" if fill == "transparent" else fill
        dash = ' stroke-dasharray="7 6"' if e.get("strokeStyle") == "dashed" else ""
        sw = e.get("strokeWidth", 2)
        t = e["type"]
        if t == "image":
            body.append(f'<image x="{e["x"]:.1f}" y="{e["y"]:.1f}" width="{e["width"]:.1f}" height="{e["height"]:.1f}" '
                        f'href="{files[e["fileId"]]["dataURL"]}"/>')
        elif t == "rectangle":
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
    order = {"<rect": 0, "<elli": 0, "<imag": 0, "<poly": 1, "<text": 2}
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
            saved = json.load(f)
        elements = saved["elements"]
        with open(path + ".svg", "w") as f:
            f.write(to_svg(elements, title, saved["files"], saved["appState"]["viewBackgroundColor"]))
        print(f"{name}: {len(elements)} elements")
