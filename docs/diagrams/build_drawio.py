#!/usr/bin/env python3
"""Builds docs/diagrams/ticketdrop-architecture.drawio: the whole platform on AWS.

    python3 docs/diagrams/build_drawio.py

AWS and Kubernetes icons are draw.io's own stencils. Logos draw.io has no
stencil for (Traefik, Argo CD, Prometheus, ...) come from Simple Icons and are
embedded in the file, so it renders with no network access.

Edge colours:
    black   customer traffic        green   delivery (Git, CI, Argo CD)
    pink    events (SNS, SQS)       purple  control (IAM, DNS, certificates, nodes)
    orange  metrics
"""
import base64
import os
import urllib.request
from xml.sax.saxutils import quoteattr

HERE = os.path.dirname(os.path.abspath(__file__))
CACHE = os.path.expanduser("~/.cache/simple-icons")

BLACK, PINK, GREEN, PURPLE, ORANGE = "#232F3E", "#E7157B", "#1E8E3E", "#7B3FE4", "#D9730D"

cells = []


def cell(cid, value, style, x, y, w, h):
    cells.append(
        f'<mxCell id="{cid}" value={quoteattr(value)} style={quoteattr(style)} parent="1" vertex="1">'
        f'<mxGeometry x="{x}" y="{y}" width="{w}" height="{h}" as="geometry"/></mxCell>')
    return cid


def edge(cid, src, tgt, label="", color=BLACK, dashed=False, width=2, out=None, into=None, via=(), at=None):
    """An orthogonal connector. out and into fix where it leaves and arrives,
    as fractions of the shape's width and height; via lists waypoints."""
    style = (f"edgeStyle=orthogonalEdgeStyle;rounded=1;html=1;strokeColor={color};strokeWidth={width};"
             f"fontSize=11;fontColor={color};labelBackgroundColor=#ffffff;endArrow=block;endFill=1;"
             + ("dashed=1;" if dashed else ""))
    if out:
        style += f"exitX={out[0]};exitY={out[1]};exitDx=0;exitDy=0;"
    if into:
        style += f"entryX={into[0]};entryY={into[1]};entryDx=0;entryDy=0;"
    points = "".join(f'<mxPoint x="{x}" y="{y}"/>' for x, y in via)
    if points:
        points = f'<Array as="points">{points}</Array>'
    # at moves the label along the line: -1 is the start, 1 the end.
    pos = f'x="{at}" ' if at is not None else ""
    cells.append(
        f'<mxCell id="{cid}" value={quoteattr(label)} style={quoteattr(style)} parent="1" '
        f'source="{src}" target="{tgt}" edge="1"><mxGeometry {pos}relative="1" as="geometry">{points}</mxGeometry></mxCell>')


LABEL = "verticalLabelPosition=bottom;verticalAlign=top;align=center;html=1;fontSize=11;fontColor=#232F3E;"


def aws(res, fill):
    return ("sketch=0;outlineConnect=0;fillColor=%s;strokeColor=#ffffff;dashed=0;aspect=fixed;"
            "shape=mxgraph.aws4.resourceIcon;resIcon=mxgraph.aws4.%s;" % (fill, res)) + LABEL


def aws_shape(shape, fill):
    return ("sketch=0;outlineConnect=0;fillColor=%s;strokeColor=none;dashed=0;aspect=fixed;pointerEvents=1;"
            "shape=mxgraph.aws4.%s;" % (fill, shape)) + LABEL


def aws_group(icon, stroke, fill="none", font=None, extra=""):
    return ("outlineConnect=0;gradientColor=none;html=1;whiteSpace=wrap;fontSize=12;fontStyle=1;"
            "shape=mxgraph.aws4.group;grIcon=mxgraph.aws4.%s;strokeColor=%s;fillColor=%s;"
            "verticalAlign=top;align=left;spacingLeft=30;fontColor=%s;dashed=0;%s"
            % (icon, stroke, fill, font or stroke, extra))


def k8s(icon):
    return ("aspect=fixed;sketch=0;html=1;dashed=0;fillColor=#2875E2;strokeColor=#ffffff;"
            "shape=mxgraph.kubernetes.icon2;prIcon=%s;" % icon) + LABEL


def box(stroke, fill, dashed=True):
    return ("rounded=1;arcSize=4;html=1;whiteSpace=wrap;fillColor=%s;strokeColor=%s;fontColor=%s;"
            "verticalAlign=top;align=left;spacingLeft=10;spacingTop=4;fontSize=12;fontStyle=1;%s"
            % (fill, stroke, stroke, "dashed=1;" if dashed else ""))


def logo(slug, colour):
    """A Simple Icons logo in its brand colour, as an embedded image style."""
    os.makedirs(CACHE, exist_ok=True)
    path = os.path.join(CACHE, slug + ".svg")
    if not os.path.exists(path):
        urllib.request.urlretrieve(f"https://cdn.jsdelivr.net/npm/simple-icons@13/icons/{slug}.svg", path)
    with open(path) as f:
        svg = f.read().replace("<svg ", f'<svg fill="{colour}" ', 1)
    data = base64.b64encode(svg.encode()).decode()
    return "shape=image;aspect=fixed;imageAspect=0;image=data:image/svg+xml," + data + ";" + LABEL


TEXT = "text;html=1;align=left;verticalAlign=middle;fontColor=#232F3E;"

# ---- title ---------------------------------------------------------------
cell("title", "TicketDrop: platform architecture on Amazon EKS", TEXT + "fontSize=22;fontStyle=1;", 20, 10, 800, 34)
cell("subtitle", "Nine Go services · event-driven over SNS and SQS · scaled by KEDA and Karpenter · delivered by GitOps",
     TEXT + "fontSize=13;fontColor=#5A6B7B;", 20, 42, 900, 22)

# ---- regions (drawn first, so everything else sits on top) ----------------
cell("g_aws", "AWS Cloud  ·  us-east-1", aws_group("group_aws_cloud_alt", "#232F3E"), 270, 230, 1400, 785)
cell("g_vpc", "VPC", aws_group("group_vpc2", "#8C4FFF", font="#8C4FFF"), 300, 275, 990, 725)
cell("g_pub", "Public subnets", aws_group("group_security_group", "#7AA116", "#F2F6E8", "#248814", "grStroke=0;"), 325, 315, 940, 130)
cell("g_prv", "Private subnets", aws_group("group_security_group", "#00A4A6", "#E6F6F7", "#147EBA", "grStroke=0;"), 325, 460, 940, 525)
cell("g_eks", "", box("#ED7100", "#FFFFFF"), 345, 495, 900, 475)
cell("i_eks", "", aws("eks", "#ED7100"), 500, 503, 34, 34)
cell("t_eks", "Amazon EKS cluster", TEXT + "fontSize=13;fontStyle=1;fontColor=#ED7100;", 542, 505, 200, 30)
SIDE = "labelPosition=right;verticalLabelPosition=middle;verticalAlign=middle;align=left;spacingLeft=6;"
BELOW = "verticalLabelPosition=bottom;verticalAlign=top;align=center;"
cell("n_sys", "System nodes: managed group, on-demand", k8s("node").replace(BELOW, SIDE), 372, 939, 26, 25)
cell("n_spot", "Workload nodes: Karpenter, Spot",
     aws("spot_instance", "#ffffff").replace("strokeColor=#ffffff", "strokeColor=#232F3E").replace(BELOW, SIDE), 700, 938, 26, 26)
cell("t_az", "spread across 3 availability zones", TEXT + "fontSize=11;fontColor=#8C4FFF;", 1090, 278, 200, 20)

cell("g_edge", "ns: traefik", box("#24A1C1", "#EFF9FC"), 362, 548, 120, 190)
cell("g_app", "ns: ticketdrop  ·  one Helm chart, nine releases", box("#2875E2", "#F0F6FF"), 495, 548, 455, 190)
cell("g_data", "ns: data", box("#2E7D32", "#F1F8F1"), 963, 548, 268, 190)
cell("g_plat", "Platform controllers  ·  every one installed by Argo CD from the GitOps repo", box("#7B3FE4", "#F6F1FE"), 362, 775, 869, 160)

# ---- edge of the system ---------------------------------------------------
cell("users", "Customers", aws("users", "#ffffff").replace("strokeColor=#ffffff", "strokeColor=#232F3E"), 70, 105, 56, 56)
cell("r53", "Route 53<br>hosted zone", aws("route_53", "#8C4FFF"), 422, 105, 56, 56)
cell("le", "Let's Encrypt<br>certificates", logo("letsencrypt", "#003A70"), 700, 100, 50, 50)
cell("nlb", "Network Load Balancer", aws_shape("network_load_balancer", "#8C4FFF"), 424, 345, 52, 52)
cell("nat", "NAT gateway<br>(one, to save cost)", aws_shape("nat_gateway", "#8C4FFF"), 1060, 345, 52, 52)

# ---- workloads --------------------------------------------------------------
cell("traefik", "Traefik<br>Gateway API<br>and TLS", logo("traefikproxy", "#24A1C1"), 425, 574, 50, 50)
row1 = ["gateway", "orders", "inventory", "payments", "fulfillment"]
row2 = ["catalog", "notifications", "scheduler", "web"]
for i, name in enumerate(row1):
    cell("p_" + name, name, k8s("pod"), 520 + i * 86, 580, 40, 38)
for i, name in enumerate(row2):
    cell("p_" + name, name, k8s("pod"), 563 + i * 86, 655, 40, 38)

cell("pg", "PostgreSQL<br>CloudNativePG", logo("postgresql", "#4169E1"), 985, 595, 46, 46)
cell("valkey", "Valkey<br>(planned)", "shape=cylinder3;whiteSpace=wrap;html=1;boundedLbl=1;size=8;fillColor=#E8F5E9;strokeColor=#2E7D32;" + LABEL, 1078, 597, 36, 44)
cell("ebs", "EBS gp3<br>volumes", aws("elastic_block_store", "#7AA116"), 1160, 595, 46, 46)

plat = [
    ("argo", "Argo CD<br>GitOps sync", logo("argo", "#EF7B4D")),
    ("karp", "Karpenter<br>adds nodes", k8s("node")),
    ("keda", "KEDA<br>scales on queue", k8s("hpa")),
    ("certm", "cert-manager<br>TLS", k8s("crd")),
    ("extdns", "external-dns<br>DNS records", k8s("crd")),
    ("csi", "Secrets Store<br>CSI driver", k8s("secret")),
    ("prom", "Prometheus<br>metrics, alerts", logo("prometheus", "#E6522C")),
    ("graf", "Grafana<br>dashboards", logo("grafana", "#F46800")),
]
for i, (cid, label, style) in enumerate(plat):
    cell(cid, label, style, 392 + i * 106, 825, 46, 46 if "kubernetes" not in style else 44)

# ---- AWS services -----------------------------------------------------------
cell("sns", "SNS topic<br>ticketdrop-events", aws("sns", "#E7157B"), 1350, 300, 56, 56)
cell("sqs", "SQS queue per consumer<br>+ dead-letter queues", aws("sqs", "#E7157B"), 1540, 300, 56, 56)
cell("s3", "S3: issued tickets<br>(planned)", aws_shape("bucket_with_objects", "#7AA116"), 1352, 470, 52, 54)
cell("sm", "Secrets Manager", aws("secrets_manager", "#DD344C"), 1540, 470, 56, 56)
cell("ecr", "ECR<br>signed images", aws("ecr", "#ED7100"), 1350, 640, 56, 56)
cell("iam", "IAM + EKS Pod Identity<br>one role per service account", aws("identity_and_access_management", "#DD344C"), 1540, 640, 56, 56)

# ---- delivery ---------------------------------------------------------------
cell("dev", "You", aws("user", "#ffffff").replace("strokeColor=#ffffff", "strokeColor=#232F3E"), 70, 1095, 52, 52)
cell("repo", "App repo<br>services + Terraform", logo("github", "#181717"), 250, 1096, 50, 50)
cell("gha", "GitHub Actions<br>test · scan · build · sign", logo("githubactions", "#2088FF"), 500, 1096, 50, 50)
cell("gitops", "GitOps repo<br>charts + values", logo("github", "#181717"), 770, 1096, 50, 50)
cell("tf", "Terraform<br>plan on PR, apply on merge", logo("terraform", "#844FBA"), 1040, 1096, 50, 50)

# ---- flows --------------------------------------------------------------------
# Lanes keep the long connectors apart: horizontal ones at y 468-488 (above the
# cluster) and y 747-765 (between the workloads and the controllers), vertical
# ones at x 1298-1330 (between the VPC and the AWS services).
T, B, L, R = (0.5, 0), (0.5, 1), (0, 0.5), (1, 0.5)
edge("e1", "users", "r53", "HTTPS", out=R, into=L)
edge("e2", "r53", "nlb", "alias record", out=B, into=T)
edge("e3", "nlb", "traefik", out=B, into=T)
edge("e4", "traefik", "p_gateway", "/v1", out=R, into=L)
edge("e5", "g_app", "pg", "SQL", BLACK, width=1, out=(1, 0.37), into=L)
edge("e6", "g_app", "sns", "publish (outbox)", PINK, out=(0.4, 0), into=L, via=[(677, 478), (1308, 478), (1308, 328)])
edge("e7", "sns", "sqs", "filtered fan-out", PINK, out=R, into=L)
edge("e8", "sqs", "g_app", "consume", PINK, out=T, into=(0.58, 0), via=[(1568, 262), (1298, 262), (1298, 468), (759, 468)])
edge("e9", "keda", "sqs", "reads queue depth", PINK, dashed=True, width=1, out=T, into=R,
     via=[(627, 757), (1320, 757), (1320, 440), (1640, 440), (1640, 328)], at=-0.75)
edge("e10", "p_fulfillment", "s3", "tickets (planned)", BLACK, dashed=True, width=1, out=T, into=L, via=[(884, 488), (1318, 488), (1318, 497)])
edge("e11", "csi", "sm", "mounts secrets", PURPLE, dashed=True, width=1, out=T, into=L,
     via=[(945, 747), (1330, 747), (1330, 585), (1490, 585), (1490, 498)], at=0.45)
edge("e12", "certm", "r53", "writes DNS records", PURPLE, dashed=True, width=1, out=T, into=R,
     via=[(733, 765), (489, 765), (489, 133)], at=-0.62)
edge("e13", "le", "r53", "checks DNS-01", PURPLE, dashed=True, width=1, out=L, into=(1, 0.3))
edge("e15", "prom", "g_app", "scrapes :9090", ORANGE, dashed=True, width=1, out=T, into=(0.9, 1), via=[(1051, 765), (904, 765)])
edge("e16", "dev", "repo", "git push", GREEN, out=R, into=L)
edge("e17", "repo", "gha", "", GREEN, out=R, into=L)
edge("e18", "gha", "ecr", "push image (OIDC, no stored keys)", GREEN, out=T, into=L, via=[(525, 1035), (1340, 1035), (1340, 668)], at=-0.45)
edge("e19", "gha", "gitops", "new image tag", GREEN, out=R, into=L)
edge("e20", "argo", "gitops", "pulls and syncs", GREEN, out=L, into=T, via=[(353, 848), (353, 1055), (795, 1055)])
edge("e21", "tf", "g_aws", "provisions", PURPLE, dashed=True, width=1, out=T, into=(0.568, 1))

# ---- legend -------------------------------------------------------------------
cell("legend", "Legend", box("#5A6B7B", "#FFFFFF", dashed=False), 1330, 1050, 340, 150)
for i, (name, colour, dashed) in enumerate([
        ("Customer traffic", BLACK, False), ("Events (SNS, SQS)", PINK, False),
        ("Delivery (Git, CI, Argo CD)", GREEN, False), ("Control (IAM, DNS, certs, nodes)", PURPLE, True),
        ("Metrics", ORANGE, True)]):
    y = 1086 + i * 22
    style = f"endArrow=block;endFill=1;html=1;strokeColor={colour};strokeWidth=2;" + ("dashed=1;" if dashed else "")
    cells.append(
        f'<mxCell id="lg{i}" style="{style}" parent="1" edge="1"><mxGeometry relative="1" as="geometry">'
        f'<mxPoint x="1348" y="{y}" as="sourcePoint"/><mxPoint x="1408" y="{y}" as="targetPoint"/></mxGeometry></mxCell>')
    cell(f"lt{i}", name, TEXT + "fontSize=11;", 1418, y - 10, 240, 20)

model = ('<mxGraphModel dx="1400" dy="900" grid="1" gridSize="10" guides="1" tooltips="1" connect="1" arrows="1" '
         'fold="1" page="1" pageScale="1" pageWidth="1700" pageHeight="1230" math="0" shadow="0"><root>'
         '<mxCell id="0"/><mxCell id="1" parent="0"/>' + "".join(cells) + "</root></mxGraphModel>")

if __name__ == "__main__":
    out = os.path.join(HERE, "ticketdrop-architecture.drawio")
    with open(out, "w") as f:
        f.write('<mxfile host="app.diagrams.net"><diagram name="TicketDrop architecture" id="ticketdrop">'
                + model + "</diagram></mxfile>")
    with open(os.path.join(HERE, ".ticketdrop-architecture.model.xml"), "w") as f:
        f.write(model)
    print(f"{out}: {len(model) // 1024} KB, {len(cells)} cells")
