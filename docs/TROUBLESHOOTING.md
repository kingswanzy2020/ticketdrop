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
