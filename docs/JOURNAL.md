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

Dates: 3 October 2026 to . Exit check: a simulated drop finishes with every
order ticketed or failed and tickets issued within capacity; killing a service
mid-run causes redelivery without duplicates; a poison message lands in the
dead-letter queue.

The service code in this phase was written for me. My part was to read it, run
it, break it and understand why it is built this way.

The record for this phase: DECISIONS 1 to 24, TROUBLESHOOTING 1 to 3, and the
runs in PROOF.md. The runs dated 6 October were made in a cloud container, not
on my machine; I have not yet run the checks myself.

### Notes
October 13, 2026: Explored all the built services, trying to understand how they all connect and work together.

### 1. The problem
 No kubernetes cluster to run the application
### 2. What I built
1. Created the cluster on Docker Desktop's kind provisioner: one node, Kubernetes 1.36.4.
2. Created ticketdrop with enforce: restricted, and data unenforced.

### 3. Why this approach
1. Wanted to create a resticted namespace for my app services, no not every pod can simply land in that namespace running as a root user.

### 4. Run a test
1. A root nginx pod was refused, as was expected in the restricted ticketdrop namespace
### 5. What I changed

### 6. At a different scale

### Evidence
check the images of phase one in "/home/king/Pictures/Screenshots/ticketdrop project Screenshots/Phase1"
---

## Phase 2: Kubernetes packaging on Docker Desktop

Dates: to . Exit check: the drop passes through the Gateway; KEDA scales
`fulfillment` 0 → N → 0 on queue depth.

### Notes
October,13 2026: Run the application locally on my docker desktop kind cluster

### 1. The problem
1. No SNS/SQS services, database, single charts
### 2. What I built
1. One cluster with six databases (Scheduler, Inventory, Fufillment, Catalog, Payments, Orders).
2. Applied the pg_hba.conf method to isolate each user to their database.
3. Images are now tagged ticketdrop/<svc>:dev, not latest, after Always and latest were ruled out.
4. The ticketdrop-gateway:dev test pod was refused until the four security settings were in the pod spec. The first manifest had them at the wrong level and written as dotted keys.
5. Deployed goaws in data with a ConfigMap, a Deployment with probes and requests, and a Service.
6. Created ticketdrop-gitops infrastructure GitHub repo.
7. Installed CloudNativePG with Helm into cnpg-system, labelled restricted.

### 3. Why this approach
1. Normally, Postgres grants each user access to all other databases, but we using the pg_hba.conf when setting up the cluster allows proper isolation of each user to their own database.
2. Remember, a databaseRole and RBAC role are slightly different. The databaseRole creates a database user, while a normal RBAC role creates a role with permissions. Also, the database role exists and applies only within the database server.
3. catalog comes from the bootstrap, and the other five have DatabaseRole and Database objects. 
4. pg_hba was chosen over REVOKE. 
5. Objects use reclaim policy retain. 
6. Passwords are kept in ignored files, with Secrets created by hand and a backup script.

### 4. What broke
1. After restricting label to the ticketdrop namespace, the test pod with the nginx image was restricted from being scheduled on the node because the image run as a root user. So, only pods that run as nonroot are allowed to be placed within that namespace, as enforced by the Pod Security Admission Controller
2. The first helm upgrade --install failed on a relative -f path.
3. The roles and databases files were first missing namespace: data.
orders reached its own database and was refused on catalog.
### 5. What I changed
1. The ConfigMap's Host was fixed to goaws.data.svc.cluster.local, and the ConfigMap needed a pod restart because of subPath.
2. Ten queues listed.
3. orders reached its own database and was refused on catalog.
### 6. At a different scale

### Evidence
1. the images(/home/king/Pictures/Screenshots/ticketdrop project Screenshots/Phase2) and evidence texts.