# Journal

One entry per phase, written by me when the phase ends and before the next one
starts. The facts live in [DECISIONS.md](DECISIONS.md),
[TROUBLESHOOTING.md](TROUBLESHOOTING.md) and [PROOF.md](PROOF.md); this page
is what I make of them.

Each entry answers the same six questions, so that at the end I can say of any
part of the system: I built this, this was the problem, this is why I chose
this approach, this is what broke, this is what I changed, and this is what I
would do differently at a different scale.

While a phase is under way, add a dated line to its **Notes** as things happen.
A surprise, a wrong assumption, a number, a dead end. One line is enough; the
entry is written from these.

---

## Entry template

```markdown
## Phase N: <name>

Dates: <start> to <end>. Exit check: <the check from the plan, and whether it passed>.

### Notes
- <date>: <what happened>

### 1. The problem
What this phase had to solve, and what would have gone wrong without it.

### 2. What I built
In a few lines. What I wrote myself and what I was given.

### 3. Why this approach
The two or three decisions that mattered most, the alternatives I turned down,
and what each choice cost. Link the DECISIONS entries.

### 4. What broke
What failed, on purpose or not, and how I found the cause. Link the
TROUBLESHOOTING entries and the evidence.

### 5. What I changed
What the failures or the measurements made me do differently.

### 6. At a different scale
What I would do for a tenth of the load, and for a hundred times it, and what
signal would tell me it was time to change.

### Evidence
Links to check runs in PROOF.md and files in `evidence/phase-N/`.
```

---

## Phase 1: Services, local

Dates: 3 October 2026 to 6 October 2026. Exit check: a simulated drop finishes with every
order ticketed or failed and tickets issued within capacity; killing a service
mid-run causes redelivery without duplicates; a poison message lands in the
dead-letter queue. It passed, and I reran it on my machine on 13 October.

The service code in this phase was written for me. My part was to read it, run
it, break it and understand why it is built this way.

The record for this phase: DECISIONS 1 to 24, TROUBLESHOOTING 1 to 3, and the
runs in PROOF.md. The runs dated 6 October were made in a cloud container, not
on my machine. I ran the checks myself on 13 October.

### Notes
October 13, 2026: Explored all the built services, trying to understand how they all connect and work together.

### 1. The problem
1. Needed to understand how the services work together, and prove they survive failures, before packaging them for Kubernetes.

### 2. What I built
1. The service code was written for me. I read through it, then ran the local stack.

### 3. Why this approach
1. The choices are in DECISIONS 1 to 24.

### 4. What broke
1. Broke it on purpose with `make drop-check`: 600 orders for 1000 tickets, with payments killed mid-run. Every accepted order finished (370 ticketed, 40 failed), replayed events changed nothing, and the poison message reached the dead-letter queue.
2. Broke it again with `make outage-check`: the payment provider down and the scheduler's leader killed. The standby took over 2s later, all 5 orders failed when their holds expired, the tickets went back to stock, and retrying the dead-lettered payments charged nobody.

### 5. What I changed
1. Nothing. Both checks passed.

### 6. At a different scale

### Evidence
The screenshots in [evidence/phase-1/](evidence/phase-1/).

---

## Phase 2: Kubernetes packaging on Docker Desktop

Dates: to . Exit check: the drop passes through the Gateway; KEDA scales
`fulfillment` 0 → N → 0 on queue depth.

### Notes
October,13 2026: Run the application locally on my docker desktop kind cluster

### 1. The problem
1. No kubernetes cluster to run the application.
2. No SNS/SQS services, database, single charts
### 2. What I built
1. Created the cluster on Docker Desktop's kind provisioner: one node, Kubernetes 1.36.4.
2. Created ticketdrop with enforce: restricted, and data unenforced.
3. One cluster with six databases (Scheduler, Inventory, Fulfillment, Catalog, Payments, Orders).
4. Applied the pg_hba.conf method to isolate each user to their database.
5. Images are now tagged ticketdrop/<svc>:dev, not latest, after Always and latest were ruled out.
6. The ticketdrop/gateway:dev test pod was refused until the four security settings were in the pod spec. The first manifest had them at the wrong level and written as dotted keys.
7. Deployed goaws in data with a ConfigMap, a Deployment with probes and requests, and a Service.
8. Created ticketdrop-gitops infrastructure GitHub repo.
9. Installed CloudNativePG with Helm into cnpg-system, labelled restricted.
10. Wrote one shared Helm chart, with a values file for each of the nine services, and installed them as nine releases.
11. Added the PDB and topology spread to the chart. Six services run two replicas, so 15 pods in total.
12. Installed the Gateway API CRDs and Traefik, then wrote the Gateway and the HTTPRoute. The shop now answers on localhost:8088 with no port-forward.

### 3. Why this approach
1. I put `enforce: restricted` on the ticketdrop namespace, so a pod has to be locked down before it can land there: `runAsNonRoot` and a `RuntimeDefault` seccomp profile on the pod, `allowPrivilegeEscalation: false` and all capabilities dropped on the container. A pod that breaks the rule is refused when it is created, not found later. The service images already run as non-root (UID 65532), so the rule costs them nothing, but Pod Security reads only the pod spec, so the four settings have to be written there even when the image is fine. The cost is that every pod in the namespace has to carry them, including throwaway test pods. data stays unenforced because the goaws image runs as root. See DECISIONS 27 and 34, and [the refused pod](evidence/phase-2/02-restricted-rejects-root-pod.txt).
2. Normally, Postgres grants each user access to all other databases, but we using the pg_hba.conf when setting up the cluster allows proper isolation of each user to their own database.
3. Remember, a databaseRole and RBAC role are slightly different. The databaseRole creates a database user, while a normal RBAC role creates a role with permissions. Also, the database role exists and applies only within the database server.
4. catalog comes from the bootstrap, and the other five have DatabaseRole and Database objects. 
5. pg_hba was chosen over REVOKE. orders reached its own database and was refused on catalog.
6. Objects use reclaim policy retain. 
7. Passwords are kept in ignored files, with Secrets created by hand and a backup script.
8. One release per service, so each one upgrades and rolls back alone (DECISIONS 35).
9. The PDB only renders above one replica, and the three queue consumers stay at one because their messages can wait in the queue (DECISIONS 39 to 41).
10. Only the Gateway API, not Traefik's own CRDs, so the routes should carry over to EKS (DECISIONS 42 to 48).

### 4. What broke
1. After restricting label to the ticketdrop namespace, the test pod with the nginx image was restricted from being scheduled on the node because the image run as a root user. So, only pods that run as nonroot are allowed to be placed within that namespace, as enforced by the Pod Security Admission Controller
2. The first helm upgrade --install failed on a relative -f path.
3. The roles and databases files were first missing namespace: data.
4. helm lint could not load my values.yaml, because of a note written as plain text (TROUBLESHOOTING 4).
5. My first PDB had no end to its if, and my first topology spread was a map with topologyKey twice. It also used zone with DoNotSchedule, which would have left every pod Pending on a node with no zone label.
6. The Traefik chart rejected my values: redirectTo does not exist, and I typed fault for false (TROUBLESHOOTING 5).
7. My HTTPRoute had an uppercase name, no namespace on the parentRef, and sent /v1 to catalog when it should go to the gateway.
### 5. What I changed
1. The ConfigMap's Host was fixed to goaws.data.svc.cluster.local, and the ConfigMap needed a pod restart because of subPath.
2. Ten queues listed.
3. Made the http port optional with `http: null`, because only five of the nine services serve 8080.
4. Moved the public port to 8088, because 8080 is taken on my machine and I cannot bind below 1024.
### 6. At a different scale

### Evidence
1. The screenshots and evidence texts in [evidence/phase-2/](evidence/phase-2/).