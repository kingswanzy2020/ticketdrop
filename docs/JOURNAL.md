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
The load test was added on 17 November, after Phase 2: `make demo-drop` must
pass with nothing oversold. It passes at 1,000, 2,000 and 4,000 customers on
compose.

The service code in this phase was written for me. My part was to read it, run
it, break it and understand why it is built this way.

The record for this phase: DECISIONS 1 to 24 and 56, TROUBLESHOOTING 1 to 3
and 8, and the runs in PROOF.md. The runs dated 6 October were made in a cloud
container, not on my machine. I ran the checks myself on 13 October. The load
test runs on 17 November were made for me on my machine.

### Notes
October 13, 2026: Explored all the built services, trying to understand how they all connect and work together.
November 17, 2026: First k6 run on the cluster, 200 customers for 200 tickets. Passed.
November 17, 2026: 1,000 customers on the cluster failed twice: 433 and then 679 customers were told the box office could not be reached while tickets were still left. Nothing was oversold.
November 17, 2026: The cause was the one stock row per tier. Every hold waited for the previous one to commit, so about 120 holds a second at most, and orders gave up on inventory after 2s.
November 17, 2026: Some of those holds still went through after orders had given up, so tickets were held with no order until the hold expired.
November 17, 2026: Chose batching holds over moving stock to Valkey. On compose, 2,000 customers went from 1,456 refused to 0, and p95 buy time from 2,013 ms to 33 ms.
November 17, 2026: 4,000 customers passed on compose. Orders now took up to 75s to finish, because fulfillment is throttled on purpose.
November 17, 2026: Restarted inventory on the cluster to measure batching there, but the pod still ran the old image. The node keeps its own copy of `dev`.
November 17, 2026: Loaded the new inventory image into the node by hand and restarted it. 1,000 customers on the cluster passed: 0 refused, p95 buy time 110 ms.
November 17, 2026: 2,000 customers on the cluster failed. Postgres ran out of connections, so orders could not write 115 orders after their holds were granted.
November 17, 2026: The same run lost most of its drop-page polls to port 8088, which closes connections above about 1,024 before they reach Traefik.
November 17, 2026: Gave each service's database pool a fixed size, 83 connections at most out of 97. 1,000 customers passed again on the cluster and Postgres peaked at 65.

### 1. The problem
1. Needed to understand how the services work together, and prove they survive failures, before packaging them for Kubernetes.
2. Needed to know what happens when a real crowd hits a drop at once, not just 600 orders placed one after another.

### 2. What I built
1. The service code was written for me. I read through it, then ran the local stack.
2. The load test was written for me: a k6 crowd in `loadtest/drop.js` that waits on the shopfront for the drop to open, buys once each and watches its order, and `make demo-drop`, which then checks the databases for overselling, unfinished orders and tickets held with no order. It runs on compose or on the cluster.
3. Inventory now grants holds in batches: every request that queued while the last batch committed goes into one transaction.

### 3. Why this approach
1. The choices are in DECISIONS 1 to 24.
2. Batching instead of Valkey, because the row lock and the `CHECK (available >= 0)` still decide who gets a ticket, and there is no second store to keep in step. The cost is that a batch fails together, and one goroutine per pod does all the granting (DECISIONS 56).
3. DECISIONS 16 had predicted this bottleneck. It now has a superseded note saying the prediction held.

### 4. What broke
1. Broke it on purpose with `make drop-check`: 600 orders for 1000 tickets, with payments killed mid-run. Every accepted order finished (370 ticketed, 40 failed), replayed events changed nothing, and the poison message reached the dead-letter queue.
2. Broke it again with `make outage-check`: the payment provider down and the scheduler's leader killed. The standby took over 2s later, all 5 orders failed when their holds expired, the tickets went back to stock, and retrying the dead-lettered payments charged nobody.
3. The load test broke it for real at 1,000 customers on the cluster. Sampling `pg_stat_activity` showed almost every session waiting on the stock row's lock, and the one holding it was flushing its commit to disk. Customers were turned away while tickets were left, and up to 32 tickets were held with no order.
4. A rebuilt `inventory:dev` image never reached the cluster, even after a rollout restart. I found it because the new batch metric was missing and the pod's image digest did not match the one I built (TROUBLESHOOTING 8). So the before and after comparison was done on compose.

### 5. What I changed
1. Nothing after the first two checks. Both passed.
2. Inventory grants holds in batches. At 2,000 customers on compose, refused buys went from 1,456 to 0 and the p95 buy time from 2,013 ms to 33 ms. drop-check for every kill, outage-check and the 400-for-100 race still pass.
3. On the cluster, 1,000 customers now pass with batching. The Postgres connections ran out at 2,000, so every pool now has a fixed size (TROUBLESHOOTING 9, DECISIONS 57). 2,000 has not been rerun, because port 8088 cannot carry more than about 1,024 customers from my machine (TROUBLESHOOTING 10).

### 6. At a different scale

### Evidence
1. The screenshots in [evidence/phase-1/](evidence/phase-1/).
2. The load test runs, before and after batching, in [PROOF.md](PROOF.md).
3. The lock waits in [4-hot-row-lock-waits.txt](evidence/phase-1/4-hot-row-lock-waits.txt), the failed cluster run in [5-demo-drop-1000-second-run.txt](evidence/phase-1/5-demo-drop-1000-second-run.txt), and the k6 summaries in [3](evidence/phase-1/3-k6-1000-customers-summary.json), [6](evidence/phase-1/6-k6-2000-one-hold-per-transaction.json), [7](evidence/phase-1/7-k6-2000-batched-holds.json) and [8](evidence/phase-1/8-k6-4000-batched-holds.json).

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
November 17, 2026: First drop-check against the cluster, with 60 orders. One check failed: payments had already finished every order when its pod was deleted. Too few orders, not a bug.
November 17, 2026: Exit drop through the Gateway: 600 orders for 1000 tickets, 418 accepted, 182 sold out, 918 tickets issued. Deleted the payments pod mid-run and every order still finished 12s later. Fulfillment went 0 to 3 to 0 by itself. drop check passed.

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
18. drop-check now runs against the cluster with `TARGET=cluster`: orders through 8088, catalog and goaws through port-forwards, SQL through kubectl exec, and the kill is a pod delete. The script change was written for me.

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
18. The kill in the cluster is `kubectl delete pod --grace-period=0 --force`, the closest thing to a node dying. The Deployment brings a new pod, so nothing has to be restarted by hand.

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
13. The 60-order drop-check failed one check because payments had no work left when it was deleted. 600 orders fixed it.
14. My first rebuild left ticketdrop stuck in Terminating. KEDA's rights over the ScaledObject come from a RoleBinding inside ticketdrop, so deleting the namespace took them away and KEDA could not drop its finalizer. Everything I installed into ticketdrop after that was refused, and db-secrets.sh stopped after catalog (TROUBLESHOOTING 6 and 7).
### 5. What I changed
1. The ConfigMap's Host was fixed to goaws.data.svc.cluster.local, and the ConfigMap needed a pod restart because of subPath.
2. Ten queues listed.
3. Made the http port optional with `http: null`, because only five of the nine services serve 8080.
4. Moved the public port to 8088, because 8080 is taken on my machine and I cannot bind below 1024.
5. The autoscaler in the values for a microservice is inverted, meaning that the replicas block in deployment template only runs when the final boolean value is true, and skipped when its false. 
6. Dry runs of charts with big CRDs now use `kubectl apply --server-side --dry-run=server`.
7. Every new call between services is now a values change, or it times out (DECISIONS 51).
8. Check a port-forward is listening before using it.
9. The exit run uses enough orders that the kill always lands mid-run.
10. Teardown now uninstalls the services before deleting their namespace, and deletes data before CloudNativePG. The order and the reasons are in the Phase 2 workbook. The rebuild commands stop at the first failure.
### 6. At a different scale

### Evidence
1. The screenshots and evidence texts in [evidence/phase-2/](evidence/phase-2/).
2. The KEDA run, 0 to 3 to 0, in [PROOF.md](PROOF.md), [10-fulfillment-scale-0-n-0.txt](evidence/phase-2/10-fulfillment-scale-0-n-0.txt) and [10-fulfillment-scaled-up.png](evidence/phase-2/10-fulfillment-scaled-up.png).
3. The exit drop in [exit-drop-through-gateway.txt](evidence/phase-2/exit-drop-through-gateway.txt), [exit-pod-deleted-mid-drop.txt](evidence/phase-2/exit-pod-deleted-mid-drop.txt) and [exit-fulfillment-scale-0-n-0.txt](evidence/phase-2/exit-fulfillment-scale-0-n-0.txt).

---

## Phase 3: CI

Dates: November 18 to November 19, 2026. Exit check: a PR touching one service builds only that service; a
failing test or a critical CVE blocks the merge; in the GitOps repo a chart
that fails lint, kubeconform or the kind install blocks the merge.

I set up the settings and the rulesets and merged the PRs. The workflows, the
scripts and the proof PRs were written for me on 19 November, and I read them
and asked why.

### Notes
November 18, 2026: Turned on SHA pinning and read-only tokens in both repos, and made the two rulesets require `ci-ok` before it existed.
November 19, 2026: The first local checks found 15 reachable vulnerabilities on Go 1.27.1 and 21 lint issues. Moved to Go 1.27.4 everywhere.
November 19, 2026: Docker Desktop was killed for memory and my build-and-scan capture ran against nothing. Reran it after starting Docker again.
November 19, 2026: `gh run list --workflow ci` found no workflow, because nothing had run it yet. Opening the PR fixed that.
November 19, 2026: Added block force pushes and restrict deletions to both rulesets, then tied `ci-ok` to GitHub Actions.
November 19, 2026: First PR run built all nine images, because `.github/` changed. 1m22s to 1m54s per image. All nine missed the Trivy cache on the first run of the day.
November 19, 2026: Merged PR #4. Proof PRs: orders comment built only orders, docs built nothing, a failing test and an old alpine base were both blocked.
November 19, 2026: The GitOps kind test failed twice before it passed: cloud-provider-kind had its own Gateway API CRDs, and shellcheck checked lib.sh alone.
November 19, 2026: The NetworkPolicy probe got through from `ticketdrop`. A new pod is not policed for its first second, on kind and on Docker Desktop.
November 19, 2026: Third kind run passed: everything Available in 99s, 16 of 16 checks, 3m27s for the job.
November 19, 2026: GitOps proof PR set orders' pool size to "10x". Lint passed, orders crashed on start, the kind test failed and the PR was blocked. rebuild.sh also blamed three services that were fine.

### 1. The problem
1. "It works" meant I ran the checks on my laptop. Nothing stopped a broken commit reaching `main`.
2. The GitOps repo had nothing checking that the manifests are valid or that the stack comes up from them.

### 2. What I built
1. The repo settings and the two rulesets, by hand.
2. The app CI was written for me: `changes` works out which images to build, `go` runs vet, lint, `-race` tests and govulncheck, `build` builds and scans each changed image for both CPUs, and `ci-ok` is the one required check.
3. The GitOps CI was written for me: `lint` runs shellcheck and `scripts/validate.sh`, and `kind` installs the whole stack on a kind cluster with my own `rebuild.sh` and `check.sh`.

### 3. Why this approach
1. Our own change-detection script, because each Dockerfile copies only `go.mod`, `go.sum`, `pkg/` and its own service, and doubt builds all nine (DECISIONS 59).
2. One required `ci-ok` per repo that checks each job's result, and only GitHub Actions may report it (DECISIONS 58, 64).
3. Trivy from its image by digest, failing only on a fixable CRITICAL, after its action's tags were hijacked in March (DECISIONS 63).
4. The kind test runs my scripts unchanged, so a fix at home is a fix in CI (DECISIONS 67).

### 4. What broke
1. Go 1.27.1 had 15 reachable vulnerabilities and the first lint run found 21 issues (TROUBLESHOOTING 12, 13).
2. Docker Desktop was killed for memory while I captured evidence (TROUBLESHOOTING 14).
3. cloud-provider-kind installed Gateway API CRDs of its own, and `rebuild.sh` could not apply ours over them (TROUBLESHOOTING 16).
4. The NetworkPolicy probe was let through. A new pod has about a second before its policies apply, which DECISIONS 49 had missed (TROUBLESHOOTING 18).
5. My one-service proof PR failed `make vet` on its own comment. The check worked (TROUBLESHOOTING 19).
6. With only orders crashing, `rebuild.sh` said four services had timed out. The pod list showed the real one (TROUBLESHOOTING 20).

### 5. What I changed
1. Go 1.27.4 and `x/text` v0.41.0 in `go.mod` and all nine Dockerfiles, base images pinned by digest.
2. Runs on `main` are grouped by commit so none is cancelled, and `main` is rebuilt and scanned every Monday (DECISIONS 65).
3. cloud-provider-kind runs with `--gateway-channel disabled`, and the probe waits 5s before it calls.

### 6. At a different scale

### Evidence
1. Phase 2 merged before this phase started, in [00-phase-2-merged.txt](evidence/phase-3/00-phase-2-merged.txt), and the repo settings in [00-actions-settings.txt](evidence/phase-3/00-actions-settings.txt).
2. The local checks, the first PR run and the proof PRs in [evidence/phase-3/](evidence/phase-3/).
3. The exit check in [PROOF.md](PROOF.md).
4. The kind install test in [08-kind-install-test.txt](evidence/phase-3/08-kind-install-test.txt) and the new-pod window in [fail-8-networkpolicy-new-pod-window.txt](evidence/phase-3/fail-8-networkpolicy-new-pod-window.txt).

## Phase 4: AWS with Terraform

Dates: <start> to <end>. Exit check: `make aws-up` gives Ready nodes, and
`make aws-down` leaves nothing billing. Both are timed.

I did step 1 by hand, wrote the step 5 jobs, and ran every apply and destroy
from my laptop. Steps 2, 3, 4, 6 and 7, and the fixes to my step 5 jobs, were
written for me.

### Notes
November 22, 2026: The account check found a dangling delegation on `kahmedt.com`: Porkbun still named the name servers of a deleted Route 53 zone. Made a new zone and pointed Porkbun at it the same day.
November 22, 2026: Set up IAM Identity Center and the `ticketdrop` SSO profile, so the CLI no longer needs a stored key.
November 23, 2026: The first orphan check found a multi-Region KMS key that Identity Center's setup had left unused, with a replica in us-west-2. Scheduled both for deletion (TROUBLESHOOTING 21).
November 23, 2026: Applied the state bucket, 6 resources, then moved its own state into it. The plan after the move changed nothing (DECISIONS 68).
November 24, 2026: Applied `live/foundation`: imported the zone, the budget and the OIDC provider, and added the three CI roles and nine ECR repositories (DECISIONS 69).
November 25, 2026: The CI roles trusted the wrong `sub` format. This repo issues GitHub's immutable subjects, which carry the owner and repo IDs, so every role would have refused CI. Fixed and applied before the first run.
November 26, 2026: My step 5 jobs had five bugs, all found before the first run: an empty `AWS_REGION`, the budget email missing from the plan, tflint not finding its config, Trivy failing on the state bucket's key, and a summary full of no-op resources.
November 26, 2026: PR #17's plan ran as the plan role, the merge applied as the apply role with no changes, and a throwaway PR #18 showed a PR cannot take the apply role (DECISIONS 70).
November 27, 2026: Applied the network: 24 resources, the NAT took 1m57s. Plan clean afterwards, and every subnet tagged (DECISIONS 71).
November 27, 2026: My destroy sat at a confirmation prompt the pipe was hiding, for almost two hours, and left the state lock behind when it died. Released the lock, destroyed with `-auto-approve`, orphan check exit 0. About $0.10 (TROUBLESHOOTING 22).
November 27, 2026: Step 7's EKS plan is 60 to add. Trivy raised two CRITICALs inside the EKS module, the public endpoint and the nodes' open egress, both accepted with a reason (DECISIONS 72, 73).
November 27, 2026: Applied EKS: 60 resources in 848s, both nodes Ready and arm64. Then the NetworkPolicy check failed before the policy: pods on different nodes could not reach each other on port 80. The module's node security group only opens 53 and 1025-65535 between nodes (TROUBLESHOOTING 23).
November 27, 2026: After the fix: default-deny blocked the probe. The new-pod window is on EKS too: in standard mode a new pod's first three calls, about 1.5s, got through; in strict mode none did (TROUBLESHOOTING 18). Keeping standard.
November 27, 2026: Changed my mind and made strict the default. A pod getting traffic out before its policy applies is not what I want, and our services retry their startup connections anyway.

### 1. The problem

### 2. What I built

### 3. Why this approach

### 4. What broke

### 5. What I changed

### 6. At a different scale

### Evidence
