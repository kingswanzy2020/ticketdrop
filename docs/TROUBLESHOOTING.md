# Troubleshooting

Failures that actually happened while building this project: what was seen,
what caused it, and what fixed it. Oldest first.

## 1. Docker Desktop refuses a bind mount from `/tmp`

**Seen.** Starting a container with a config file mounted from `/tmp` failed:

```
The path /tmp/.../goaws.yaml is not shared from the host and is not known to Docker.
```

**Cause.** Docker Desktop runs containers inside a virtual machine and shares
only some host directories with it. The home directory is shared by default;
`/tmp` is not.

**Fix.** Files that are mounted into containers live inside the repository,
which is under the home directory (`deploy/local/`). The alternative is to add
the path under Settings → Resources → File Sharing.

It happened again in Phase 3, mounting the local Trivy scan's folder from
`/tmp`. That scan now uses `~/.cache/td-scan` and deletes it afterwards.

## 2. goaws ignores its configuration file

**Seen.** The emulator started with none of the configured queues or topics.
Copying the file to `/conf/goaws.yaml`, the path used in older examples, failed
with `Could not find the file /conf in container`.

**Cause.** The `admiralpiett/goaws:v0.5.4` image reads
`/app/conf/goaws.yaml`. Listing the image's filesystem with
`docker export <container> | tar -t` showed the real path.

**Fix.** Mount the file at `/app/conf/goaws.yaml`. The startup log confirms it:
`Loading config file: /app/conf/goaws.yaml`.

## 3. After a graceful stop, some orders stayed `pending` although their tickets existed

**Seen.** `fulfillment` was stopped with SIGTERM while it was working through
60 orders. It exited cleanly with code 0. Afterwards 20 orders had tickets, but
only 12 were marked `ticketed`, and 8 `ticket.issued` events sat unpublished in
its outbox. They were published when the service next started.

**Cause.** Every background worker was stopped at the same moment. The consumer
then let its running handlers finish, and those handlers wrote events to the
outbox after the relay had already stopped.

**Fix.** Workers now stop one at a time, last registered first, and the relay
makes a final pass before it returns (`pkg/platform/service`,
`pkg/platform/events`). A service registers its relay first so that it stops
last. The same test now leaves the outbox empty, with every order that has
tickets marked `ticketed`.

**Note.** Nothing was lost before the fix either: the outbox kept the events.
They were late, and with a single replica they were late until the next start.

## 4. `helm lint` cannot load the service chart's `values.yaml`

**Seen.** The first lint of `charts/service` (in `ticketdrop-gitops`) failed
before checking any template
([`evidence/phase-2/fail-6-values-yaml.txt`](evidence/phase-2/fail-6-values-yaml.txt)):

```
[ERROR] values.yaml: unable to parse YAML: error converting YAML to JSON: yaml: line 24: could not find expected ':'
Error: 1 chart(s) linted, 1 chart(s) failed
```

**Cause.** Line 24 was a note written as plain text under a service
(`resources.requests, and limits if you want them`). YAML read it as a key
with no `:` after it. The same kind of line appeared under every service.

**Fix.** `values.yaml` now holds only `key: value` pairs, with notes in `#`
comments. The chart was also changed to one release per service, with shared
defaults in `values.yaml`. `helm lint` passes, leaving only the
`icon is recommended` INFO line.

## 5. The Traefik chart rejects `platform/traefik/values.yaml`

**Seen.** Rendering chart 42.1.3 stopped before producing anything
([`evidence/phase-2/fail-8-traefik-values-schema.txt`](evidence/phase-2/fail-8-traefik-values-schema.txt),
a re-run on a copy with the two original lines restored):

```
Error: values don't meet the specifications of the schema(s) in the following chart(s):
traefik:
- at '/ports/web': additional properties 'redirectTo' not allowed
- at '/ports/websecure/expose/default': got string, want boolean
```

**Cause.** The chart ships a JSON schema that refuses unknown keys and wrong
types. `ports.web.redirectTo` does not exist in this chart version, and
`expose.default` under `websecure` was misspelled as `fault`, which YAML reads
as a string.

**Fix.** Removed `redirectTo`, which would also have sent traffic to HTTPS with
no TLS locally, and set `default: false`. The chart renders and a server-side
dry run passes.

**Note.** The check happens before anything reaches the cluster, so a misspelled
key here costs a re-run and leaves nothing half installed.

## 6. The rebuild left `ticketdrop` stuck in `Terminating`, and every install into it failed

**Seen.** In the Phase 2 exit rebuild (17 November 2026),
`kubectl delete ns ticketdrop --timeout=180s` timed out. The teardown carried
on, and every later object meant for `ticketdrop` was refused
([`evidence/phase-2/fail-exit-rebuild-namespace-stuck.txt`](evidence/phase-2/fail-exit-rebuild-namespace-stuck.txt)):

```
error: timed out waiting for the condition on namespaces/ticketdrop
...
NamespaceFinalizersRemaining: Some content in the namespace has finalizers remaining: finalizer.keda.sh in 2 resource instances
...
Error: INSTALLATION FAILED: failed to create resource: rolebindings.rbac.authorization.k8s.io "keda-operator" is forbidden: unable to create new content in namespace ticketdrop because it is being terminated
```

The ScaledObject and TriggerAuthentication `fulfillment` kept
`finalizer.keda.sh`, although the KEDA operator ran for over three minutes after
the delete started. Later, `helm uninstall keda --wait` ended with
`context deadline exceeded`. The reinstall of KEDA failed with no pods left, as
did all nine service installs and the `ticketdrop` HTTPRoute and NetworkPolicies.

**Cause.** With `watchNamespace: ticketdrop` (entry 52), the KEDA chart gives
the operator its rights over ScaledObjects through a RoleBinding `keda-operator`
in `ticketdrop` itself. Its only ClusterRoleBinding covers cluster-scoped kinds.
Deleting the namespace deletes that RoleBinding along with the rest, so the
operator could no longer update the two objects to drop its finalizer. That the
RoleBinding lives in `ticketdrop` is verified from `helm template`. That it went
before the ScaledObject was finalised is inferred: the operator's logs went with
the `keda` namespace and the 403s were not captured. The `helm uninstall keda`
timeout was not diagnosed.

**Fix.** Remove what KEDA owns while its rights still exist: `helm uninstall`
the nine releases first, wait until no ScaledObject is left, and only then
delete the namespace. The stuck run was recovered by removing the two
finalizers by hand. That is safe here because the finalizer only cleans up the
HPA KEDA made, which the namespace deletion had already removed.

**Note.** The capture script ran every command even after one failed, so it
printed `everything Available` after a run where nothing was installed. Stop at
the first failure.

## 7. `db-secrets.sh` stopped after the first Secret and left a password in `/tmp`

**Seen.** In the same rebuild, `scripts/db-secrets.sh` created `data/pg-catalog`,
then stopped when `ticketdrop/catalog-db` was refused, because `ticketdrop` was
still terminating (entry 6). The CNPG cluster bootstrapped from `pg-catalog`,
but the other five DatabaseRoles reported `secrets "pg-<svc>" not found`. A
file `/tmp/tmp.*` (mode 0600) held catalog's full `DATABASE_URL`, password
included.

**Cause.** The script runs under `set -e`. The failed `kubectl apply` exited it
before the next service's Secrets were made, and before the `rm` of the file the
URL had been written to.

**Fix.** The rebuild runs the script again once `ticketdrop` exists, and deletes
the leftover file. Not yet changed in the script: a `trap` that removes the file
on exit would stop a failure leaving it behind.

## 8. A rebuilt `dev` image is not what the cluster runs, even after a restart

**Seen.** On 17 November 2026 `inventory` was rebuilt with `make up` and the
cluster's Deployment restarted with `kubectl rollout restart`. The new pods
lacked the new code's metric `inventory_hold_batch_size`, and two load runs
against them behaved exactly like the old code. The pods reported image
`docker.io/ticketdrop/inventory@sha256:de1a73d7...`, while the host's
`ticketdrop/inventory:dev` was `sha256:c647e5ae...`, built minutes before.

**Cause.** The cluster's node, `desktop-control-plane` (containerd 2.3.4), has
its own image store. `crictl images` on it lists `ticketdrop/inventory:dev` as a
separate, older image (`d44ffd4d25835`). With `pullPolicy: IfNotPresent` (entry
37) the restarted pods used that copy, and nothing loads the host's rebuild into
the node. Entry 37 records the orders pod running the image built by `make up`;
how that happened then is not known, so either the setup changed since or that
observation needs checking.

**Fix.** Not fixed. Loading images into the node belongs to the cluster setup.
Until it is, cluster runs measure whatever image the node already has, and the
load-test comparison was made on compose instead.

Done by hand once, later the same day, for `inventory` only: rebuilt with
`docker compose -f deploy/local/compose.yaml build inventory` (host image
`sha256:a8734170...`), loaded with `docker save ticketdrop/inventory:dev |
docker exec -i desktop-control-plane ctr -n k8s.io images import -`, then
`kubectl -n ticketdrop rollout restart deployment/inventory`. Both new pods
reported image `sha256:50ad279e...`, the node's copy of that build, and served
`inventory_hold_batch_size`. Every later rebuild needs the same load, for every
service.

## 9. `orders` could not connect to Postgres at 2,000 customers on the cluster

**Seen.** In the load run `load-1794918941` (17 November 2026, 2,000 customers,
batched holds) 115 customers were refused and 314 tickets were left held with
no order. `orders` logged 103 errors of the form `failed to connect to
user=orders database=orders ... FATAL: remaining connection slots are reserved
for roles with the SUPERUSER attribute (SQLSTATE 53300)` or `sorry, too many
clients already`, and answered those buys with 503. The same run at 1,000
customers passed.

**Cause.** All six databases are on one Postgres (`pg-1`), with
`max_connections` 100, of which 3 are reserved for the superuser. No service
sets a pool size, so each pod's pgx pool may open `max(4, NumCPU)`
connections, 16 on this 16-CPU node. Catalog, orders, inventory and scheduler
run two pods each and payments one, so those pools alone may open 144, and
fulfillment adds 16 per pod as KEDA scales it. Pools keep idle connections, so
what a pod opened under load stays open: after the run, idle, the server held
32 for catalog (both pools full, from the shopfront's polling of the drop
page), 26 for inventory, 25 for orders, 9 for payments and 3 for scheduler.
Once the total reached 97, any pool that needed one more connection was
refused. For `orders` that happened after `inventory` had granted the hold,
which is how holds were left with no order (DECISIONS 15).

**Fix.** Each service's pool now has a fixed size (`DATABASE_MAX_CONNS`,
DECISIONS 57), adding up to 83 at most. The 1,000-customer run passed again
with it, Postgres peaking at 65 client connections, and `make drop-check`
passed on compose. The 2,000-customer run has not been repeated, because port
8088 cannot carry it (TROUBLESHOOTING 10). Evidence:
[`11-postgres-connections-exhausted.txt`](evidence/phase-1/11-postgres-connections-exhausted.txt),
[`14-postgres-connections-capped-pools.txt`](evidence/phase-1/14-postgres-connections-capped-pools.txt).

## 10. Port 8088 closes connections above about 1,024 before they reach Traefik

**Seen.** In the same run, 162,354 of k6's 188,490 requests failed with `EOF`,
every one a poll of the drop page through `host.docker.internal:8088`, in
bursts of 976 at a time out of 2,000 customers. The 1,000-customer run had
none.

**Cause.** A probe held a fixed number of connections open through port 8088
(each one a `GET /` a second for 20 s) and compared k6's count with Traefik's
`traefik_entrypoint_requests_total`. With 900 connections nothing failed and
Traefik counted all 17,064 requests. With 1,100, 1,520 requests ended in
`EOF` (about 76 a second, 1,100 less 1,024), and Traefik counted 19,482 of
21,002: exactly the ones that did not fail. So the connections over about
1,024 are closed between the host's port and Traefik, in Docker Desktop's
forwarding to the LoadBalancer address (`172.18.0.2`), not in TicketDrop.
Traefik itself has no low file limit.

**Fix.** None in this repo. A cluster run from this machine is kept to 1,000
customers or fewer, or has to start k6 somewhere that reaches Traefik without
going through port 8088. Evidence:
[`12-port-8088-connection-ceiling.txt`](evidence/phase-1/12-port-8088-connection-ceiling.txt).

## 11. govulncheck v1.1.4 panics on this module

**Seen.** `go run golang.org/x/vuln/cmd/govulncheck@v1.1.4 ./...` stopped
before reporting anything:

```
panic: unexpected expr: *ast.KeyValueExpr
```

**Cause.** v1.1.4 is an old release, picked from an unsorted list of tags. It
is built on an old `golang.org/x/tools` (v0.29.0), and the panic comes from
its source analysis meeting code it does not expect. That the newer Go is what
it cannot handle is inferred, not traced.

**Fix.** govulncheck v1.9.0, chosen from `go list -m -versions`; v1.10.0 was a
day old (DECISIONS 62). Evidence:
[`fail-3-go-checks-on-main.txt`](evidence/phase-3/fail-3-go-checks-on-main.txt).

## 12. govulncheck found 15 vulnerabilities the code reaches

**Seen.** On `main` at Go 1.27.1, govulncheck v1.9.0 exited 3 with "Your code
is affected by 15 vulnerabilities from 1 module and the Go standard library":
14 in the standard library (`net/http`, its HTTP/2 code, `crypto/tls`,
`crypto/x509`, `net/textproto`, `net/http/httputil` and `html/template`),
fixed in 1.27.2, 1.27.3 or 1.27.4, and GO-2026-6629 in
`golang.org/x/text@v0.40.0`, fixed in v0.41.0.

**Cause.** Phase 2 was built on Go 1.27.1, and three patch releases had come
out since. Nothing checked for vulnerabilities before this phase.

**Fix.** Go 1.27.4 in `go.mod` and all nine Dockerfiles, and `x/text` v0.41.0
(DECISIONS 60). govulncheck now reports "No vulnerabilities found". Evidence:
[`fail-3-go-checks-on-main.txt`](evidence/phase-3/fail-3-go-checks-on-main.txt),
[`03-go-checks-local.txt`](evidence/phase-3/03-go-checks-local.txt).

## 13. golangci-lint found 21 issues on its first run

**Seen.** golangci-lint v2.14.0 on `main`, with no config, reported 21
issues: 20 errcheck and 1 staticcheck.

**Cause.** The errcheck ones were return values nobody checked: three of
pgx's deferred `tx.Rollback`, four `Close` calls (three response bodies in the
gateway's tests, one connection in the scheduler), and thirteen others:
`io.WriteString`, `io.Copy`, `w.Write`, JSON `Encode`, `Decode` and
`Unmarshal`, the advisory unlock's `conn.Exec` and the admin server's
`Shutdown`, in `pkg/platform` and in the orders, scheduler and web code and
tests. The staticcheck one (SA4010) was an `orderIDs`
slice in `services/inventory/internal/stock/batch.go` that was appended to and
never read.

**Fix.** `.golangci.yml` excludes the common idioms and pgx's `Rollback`, and
the remaining calls ignore the error explicitly with `_ =` (DECISIONS 61). The
unused slice was removed. The run now reports `0 issues.` Evidence:
[`fail-3-go-checks-on-main.txt`](evidence/phase-3/fail-3-go-checks-on-main.txt),
[`03-go-checks-local.txt`](evidence/phase-3/03-go-checks-local.txt).

## 14. Docker Desktop was killed for memory, and a capture ran against it

**Seen.** The local build-and-scan capture (`04`) failed on every Docker
command:

```
failed to connect to the docker API at unix:///home/king/.docker/desktop/docker.sock;
... dial unix /home/king/.docker/desktop/docker.sock: connect: no such file or directory
```

`systemctl --user status docker-desktop` showed `failed (Result: oom-kill)
since Thu 2026-11-19 16:40:50 KST`, an hour before the capture ran.

**Cause.** At 16:40:45 the laptop ran out of memory (`global_oom`, 30 GB of RAM
and 8 GB of swap). The allocation that triggered it came from `code`
(VS Code), and the kernel killed the largest process it could, Docker
Desktop's virtual machine (`qemu-system-x86`, 2.0 GB anonymous and 5.4 GB
shared memory resident). What was using the rest of the memory was not
captured.

**Fix.** Docker Desktop was started again (19:07) and the capture rerun at
19:26, which passed. The capture script does not check that Docker is running
first, so a stopped daemon leaves a file full of errors that looks like a
capture. Evidence:
[`fail-3-docker-desktop-oom.txt`](evidence/phase-3/fail-3-docker-desktop-oom.txt).

## 15. `gh run list --workflow ci` cannot find the workflow

**Seen.** After `phase-3-ci` was pushed:

```
could not find any workflows named ci
```

`gh workflow list` and `gh run list` were both empty.

**Cause.** `ci.yaml` starts only on a pull request or a push to `main`, so
pushing a feature branch started nothing. GitHub lists a workflow only once
it is on the default branch or has run at least once, so until then it does
not exist by name.

**Fix.** Open the pull request; its first run registers the workflow.
