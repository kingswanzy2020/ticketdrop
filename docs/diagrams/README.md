# Diagrams

Pick whichever suits the document you are writing.

| File | What it shows | Style | Edit with |
|---|---|---|---|
| `ticketdrop-architecture.drawio` (+ `.png`) | The whole platform on AWS: network, EKS, the nine services, controllers, AWS services, delivery | Formal, AWS and Kubernetes icons | [app.diagrams.net](https://app.diagrams.net), or the draw.io VS Code extension |
| `phase2-cluster.excalidraw` (+ `.svg`, `.png`) | The system on Docker Desktop Kubernetes | Hand-drawn house style, with logos | [excalidraw.com](https://excalidraw.com), or the Excalidraw VS Code extension |
| `order-journey.excalidraw` (+ `.svg`) | One order's path through the services | Hand-drawn house style, with logos | Excalidraw |
| `aws-target.excalidraw` (+ `.svg`) | The AWS platform, in the hand-drawn style | Hand-drawn house style, with logos | Excalidraw |
| `delivery.excalidraw` (+ `.svg`) | How a change reaches the cluster | Hand-drawn house style, with logos | Excalidraw |
| `scaling-loop.excalidraw` (+ `.svg`) | KEDA and Karpenter during a drop | Hand-drawn house style, with logos | Excalidraw |

All of them are generated, so change the script and rebuild; an edit made by hand
is overwritten by the next build.

```bash
python3 docs/diagrams/build.py          # the Excalidraw diagrams and their SVGs
python3 docs/diagrams/build_drawio.py   # the draw.io diagram
```

In the draw.io diagram, Valkey and the S3 ticket store are marked "planned":
no service uses them yet.
