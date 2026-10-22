# Phase 2 commands

The commands for each step, to type by hand. Run them from the root of
`ticketdrop-gitops/` unless a step says otherwise. Each one can be run again
safely: a rerun changes nothing that is already right.

`ticketdrop-gitops/scripts/db-secrets.sh` runs the same Secret commands from
step 5 in one go. It is there as a backup, for a rebuild.

## Step 5: Postgres, run by CloudNativePG

The services and their databases. Every role has the same name as its database.

```sh
SVCS="catalog orders inventory payments fulfillment scheduler"
```

### 5.1 One password per service

Each password is made once and kept in `secrets/`, which `.gitignore` keeps out
of git. Both Secrets for a service are made from this file, so they always
agree. Hex characters never need escaping inside a URL.

```sh
mkdir -p secrets
for svc in $SVCS; do
  [ -f "secrets/$svc.password" ] || openssl rand -hex 24 | tr -d '\n' > "secrets/$svc.password"
done
```

Check that git ignores them:

```sh
git check-ignore -v secrets/catalog.password
```

### 5.2 Login Secrets in `data`

CloudNativePG reads a role's password from a `kubernetes.io/basic-auth` Secret
in the cluster's namespace, with `username` equal to the role name.
`--dry-run=client -o yaml | kubectl apply` creates the Secret, or updates it on
a rerun. `--from-file` keeps the password out of the shell history.

```sh
for svc in $SVCS; do
  kubectl create secret generic "pg-$svc" -n data \
    --type=kubernetes.io/basic-auth \
    --from-literal=username="$svc" \
    --from-file=password="secrets/$svc.password" \
    --dry-run=client -o yaml | kubectl apply -f -
  # Tells the operator to apply a password change straight away.
  kubectl label secret "pg-$svc" -n data cnpg.io/reload=true --overwrite
done
```

Check without printing a password:

```sh
kubectl get secret -n data -l cnpg.io/reload=true
kubectl describe secret pg-catalog -n data     # shows key names and byte counts only
```

### 5.3 Finish `platform/postgres/cluster.yaml`

`pg-catalog` must exist before the Cluster is applied, because the bootstrap
runs only once. Add the Secret under `bootstrap.initdb`, and the two `pg_hba`
lines under `postgresql`:

```yaml
  bootstrap:
    initdb:
      database: catalog
      owner: catalog
      secret:
        name: pg-catalog

  postgresql:
    parameters:
      shared_buffers: "256MB"
    pg_hba:
      # A role may connect over TLS only to the database with its own name.
      - hostssl sameuser all all scram-sha-256
      # Every other network connection is refused.
      - host all all all reject
```

### 5.4 Apply the Cluster

```sh
kubectl apply --dry-run=server -f platform/postgres/cluster.yaml
kubectl apply -f platform/postgres/cluster.yaml
kubectl get cluster pg -n data -w            # wait for "Cluster in healthy state"
kubectl get pods,pvc,svc -n data
```

What the operator creates from the Cluster, and how the names come about:

- `pg-1` is a pod, the first Postgres instance, named `<cluster>-<serial>`. Its
  volume is also `pg-1`. A replacement instance gets the next number (`pg-2`,
  `pg-3`), never an old one.
- The operator creates three Services as soon as it reconciles the Cluster,
  before or alongside the first pod. Nobody writes them.
  - `pg-rw`: the current primary only. Selector
    `cnpg.io/cluster: pg, cnpg.io/instanceRole: primary`.
  - `pg-ro`: replicas only (`instanceRole: replica`). It has no endpoints while
    there is one instance.
  - `pg-r`: any instance (`cnpg.io/podRole: instance`).
- On a failover the operator moves the `instanceRole: primary` label to the
  promoted pod, and `pg-rw` follows it. So services connect to
  `pg-rw.data.svc.cluster.local`, never to a pod name.

```sh
kubectl get svc pg-rw -n data -o jsonpath='{.spec.selector}'
kubectl get pods -n data -L cnpg.io/instanceRole
```

If it stalls:

```sh
kubectl describe cluster pg -n data
kubectl get events -n data --sort-by=.lastTimestamp | tail -20
```

### 5.5 Roles and databases for the other five

`catalog` was made by the bootstrap. Write one `DatabaseRole` and one
`Database` for each of the other five, in `platform/postgres/roles.yaml` and
`platform/postgres/databases.yaml`.

- `DatabaseRole`: `cluster.name: pg`, `name`, `login: true` (it is false by
  default) and `passwordSecret.name: pg-<svc>`.
- `Database`: `cluster.name: pg`, `name` and `owner`, where the owner is the
  role of the same name.

To read the fields:

```sh
kubectl explain databaserole.spec --api-version=postgresql.cnpg.io/v1
kubectl explain database.spec --api-version=postgresql.cnpg.io/v1
```

Apply and check. All ten rows should show `true` under `APPLIED`:

```sh
kubectl apply --dry-run=server -f platform/postgres/roles.yaml -f platform/postgres/databases.yaml
kubectl apply -f platform/postgres/roles.yaml -f platform/postgres/databases.yaml
kubectl get databaserole,database -n data
```

### 5.6 Check the access rules

The two `pg_hba` lines must come before the operator's last
`host all all all scram-sha-256` row:

```sh
kubectl exec -n data pg-1 -c postgres -- \
  psql -c 'select line_number, type, database, user_name, auth_method from pg_hba_file_rules'
```

### 5.7 A DATABASE_URL Secret per service in `ticketdrop`

The services run in `ticketdrop`, and a pod can read Secrets only from its own
namespace. `sslmode=require` is needed, because `pg_hba` refuses connections
without TLS.

```sh
for svc in $SVCS; do
  kubectl create secret generic "$svc-db" -n ticketdrop \
    --from-literal=DATABASE_URL="postgres://$svc:$(cat "secrets/$svc.password")@pg-rw.data.svc.cluster.local:5432/$svc?sslmode=require" \
    --dry-run=client -o yaml | kubectl apply -f -
done
```

This puts the password on the command line, where `ps` can see it for a moment.
That is acceptable on a laptop. Do not do it on a shared machine.

Check without printing the URL:

```sh
kubectl describe secret orders-db -n ticketdrop    # DATABASE_URL: <n> bytes
```

### 5.8 The done-when test

Terminal 1:

```sh
kubectl port-forward -n data svc/pg-rw 15432:5432
```

Terminal 2, from `ticketdrop-gitops/`. `PGPASSWORD` keeps the password out of
the command:

```sh
export PGPASSWORD="$(cat secrets/orders.password)"
psql "host=127.0.0.1 port=15432 user=orders dbname=orders sslmode=require" \
  -c 'select current_user, current_database();'     # works: orders | orders
psql "host=127.0.0.1 port=15432 user=orders dbname=catalog sslmode=require" \
  -c 'select 1;'                                     # refused by pg_hba.conf
unset PGPASSWORD
```

### 5.9 Capture the evidence

Run from `ticketdrop-gitops/`. The files go into the app repo.

```sh
EV=../ticketdrop/docs/evidence/phase-2

{ date -u; kubectl get cluster pg -n data; kubectl get databaserole,database -n data; } \
  2>&1 | tee "$EV/05-postgres-cluster-healthy.txt"

export PGPASSWORD="$(cat secrets/orders.password)"
{ date -u
  psql "host=127.0.0.1 port=15432 user=orders dbname=orders sslmode=require" -c 'select current_user, current_database();'
  psql "host=127.0.0.1 port=15432 user=orders dbname=catalog sslmode=require" -c 'select 1;'
} 2>&1 | tee "$EV/05-psql-own-database-only.txt"
```

Neither file may hold a password or a URL. Both of these must print `0`:

```sh
grep -c "$PGPASSWORD" "$EV"/05-*.txt
grep -c 'postgres://' "$EV"/05-*.txt
unset PGPASSWORD
```

### 5.10 Commit, without the secrets

```sh
git status --short                  # nothing under secrets/
git grep -n 'password:'             # only passwordSecret references, never a value
```

### Starting again

Deleting the `data` namespace deletes the Cluster and its data. Keep
`secrets/` and rerun 5.2 to 5.7: the same passwords go into the new Secrets,
and the bootstrap creates `catalog` again.
