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
November 13, 2026: Proved the cluster enforces NetworkPolicy. A blocked call times out after 3s, it is not refused.
November 13, 2026: Applied DNS first, then the nine service policies, then default-deny, and restarted everything. All 15 pods came back Ready.
November 14, 2026: Installed KEDA 2.21.0. The kubectl dry run failed on one CRD, but helm install was fine.
November 14, 2026: Took `replicas` out of fulfillment's Deployment. Same pod, no restart, still 1 replica.
November 14, 2026: Gave KEDA its own placeholder Secret for goaws, so it never reads fulfillment's database password.
November 15, 2026: Applied the ScaledObject on an empty queue and fulfillment went from 1 to 0 in 6s.
November 15, 2026: 1500 orders through 8088. First pod Ready in 6s, 3 pods at the peak, back to 0 pods 61s after the queue emptied. Nothing pending, nothing in the DLQs.

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
13. Wrote a NetworkPolicy template in the chart, with each service's callers and calls in its values file, plus default-deny and DNS as plain manifests in platform/network-policies/.
14. Installed KEDA with Helm into its own keda namespace, labelled restricted, watching only ticketdrop.
15. Added an autoscaling switch to the chart. With it on, the Deployment has no replicas line, so KEDA's HPA can own the count. Only fulfillment has it on.
16. Added a TriggerAuthentication and a placeholder Secret to the chart, so KEDA can sign its calls to goaws. On EKS one value switches it to an IAM role.
17. Added a ScaledObject to the chart: fulfillment scales from 0 to 5 on its queue, 100 messages per pod.

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
11. Tested NetworkPolicy on this cluster before writing any, so the rules are proven here and not left for EKS (DECISIONS 49).
12. Each service names the exact pods it may call and be called by, instead of one rule for the whole namespace, which would let a test pod reach Postgres. A test pod is now refused Postgres and goaws, and an order still ends ticketed (DECISIONS 50 and 51).
13. Default-deny and DNS cover the whole namespace, so they are plain manifests: nine releases cannot own one object (DECISIONS 50).
14. KEDA watches only ticketdrop, so a ScaledObject anywhere else is ignored. Its CRDs belong to the release, so uninstalling it deletes every ScaledObject (DECISIONS 52).
15. Left replicas out of the scaled Deployment, so a helm upgrade does not reset what the HPA set (DECISIONS 53).
16. KEDA gets its own Secret instead of reading fulfillment's env, because reading the env would also hand it the database password (DECISIONS 54).
17. Polling every 5s and a 60s cooldown, so a drop gets a pod in seconds and the pods go a minute after it, not five. On AWS that is a paid request every 5s (DECISIONS 55).

### 4. What broke
1. After restricting label to the ticketdrop namespace, the test pod with the nginx image was restricted from being scheduled on the node because the image run as a root user. So, only pods that run as nonroot are allowed to be placed within that namespace, as enforced by the Pod Security Admission Controller
2. The first helm upgrade --install failed on a relative -f path.
3. The roles and databases files were first missing namespace: data.
4. helm lint could not load my values.yaml, because of a note written as plain text (TROUBLESHOOTING 4).
5. My first PDB had no end to its if, and my first topology spread was a map with topologyKey twice. It also used zone with DoNotSchedule, which would have left every pod Pending on a node with no zone label.
6. The Traefik chart rejected my values: redirectTo does not exist, and I typed fault for false (TROUBLESHOOTING 5).
7. My HTTPRoute had an uppercase name, no namespace on the parentRef, and sent /v1 to catalog when it should go to the gateway.
8. `kubectl apply --dry-run=server` on the KEDA chart failed: the scaledjobs CRD is too big for kubectl's last-applied annotation. `--server-side` passed, and helm install never had the problem.
9. On install, the KEDA metrics server's image pull failed with a short read, so my wait timed out. It pulled on the retry and came up.
10. My first ScaledObject sat in the chart's templates with a fixed name and no triggers, so every release would have tried to create it. I also put autoscaling in KEDA's values, where it does nothing. Both were caught before any upgrade.
11. A pod watch I left running in the background kept my capture script from finishing, until I killed it.
12. A port-forward started in the background was not ready when the next curl ran, so it failed to connect. Waiting a few seconds fixed it.
### 5. What I changed
1. The ConfigMap's Host was fixed to goaws.data.svc.cluster.local, and the ConfigMap needed a pod restart because of subPath.
2. Ten queues listed.
3. Made the http port optional with `http: null`, because only five of the nine services serve 8080.
4. Moved the public port to 8088, because 8080 is taken on my machine and I cannot bind below 1024.
5. The autoscaler in the values for a microservice is inverted, meaning that the replicas block in deployment template only runs when the final boolean value is true, and skipped when its false. 
6. Dry runs of charts with big CRDs now use `kubectl apply --server-side --dry-run=server`.
7. Every new call between services is now a values change, or it times out (DECISIONS 51).
8. Check a port-forward is listening before using it.
### 6. At a different scale

### Evidence
1. The screenshots and evidence texts in [evidence/phase-2/](evidence/phase-2/).
2. The KEDA run, 0 to 3 to 0, in [PROOF.md](PROOF.md), [10-fulfillment-scale-0-n-0.txt](evidence/phase-2/10-fulfillment-scale-0-n-0.txt) and [10-fulfillment-scaled-up.png](evidence/phase-2/10-fulfillment-scaled-up.png).