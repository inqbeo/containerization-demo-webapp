# todo — Helm chart

Helm chart for the Inqbeo **todo** demo app (the running example of the Inqbeo
containerization & Kubernetes course).

It can run the app three ways:

- **SQLite** on a PersistentVolume (default) — simplest, single-replica.
- **Postgres included** — the chart deploys a Postgres next to the app.
- **External Postgres** — point at an existing database via a Secret. Designed
  for a [CloudNativePG](https://cloudnative-pg.io/) (CNPG) *app* Secret: the
  chart reads the connection details straight from it.

It can expose the app via a classic **Ingress** or a Gateway API **HTTPRoute**.

---

## Install

The chart is published from GitHub two ways — use whichever you prefer.

### A) OCI registry (GHCR)

```sh
helm install todo oci://ghcr.io/inqbeo/charts/todo --version 0.1.0
```

### B) Classic Helm repo (GitHub Pages)

```sh
helm repo add inqbeo https://inqbeo.github.io/containerization-demo-webapp
helm repo update
helm install todo inqbeo/todo
```

---

## Common scenarios

### 1. Default — SQLite on a PVC

```sh
helm install todo oci://ghcr.io/inqbeo/charts/todo \
  --set app.companyName="Inqbeo" \
  --set app.theme=coral
```

The login password is generated on first start; read it from the log:

```sh
kubectl logs deploy/todo | grep password
```

### 2. Postgres included (chart deploys Postgres)

```sh
helm install todo oci://ghcr.io/inqbeo/charts/todo \
  --set database.type=postgres \
  --set database.postgres.mode=internal \
  --set database.postgres.internal.persistence.size=5Gi
```

### 3. External Postgres via a CloudNativePG Secret

CNPG creates an *app* Secret (e.g. `todo-db-app`, type `kubernetes.io/basic-auth`)
containing a ready-to-use `uri` plus `host`, `port`, `username`, `password`,
`dbname`. The chart reads everything from there — **no DB credentials in your
values**.

```sh
helm install todo oci://ghcr.io/inqbeo/charts/todo \
  --set database.type=postgres \
  --set database.postgres.mode=external \
  --set database.postgres.external.existingSecret=todo-db-app
```

By default the chart uses the `uri` key. To assemble the DSN from individual
keys instead (for non-CNPG secrets), set `external.uriKey=""` and adjust the
`*Key` fields:

```sh
  --set database.postgres.external.uriKey="" \
  --set database.postgres.external.hostKey=host \
  --set database.postgres.external.userKey=username \
  --set database.postgres.external.passwordKey=password \
  --set database.postgres.external.dbnameKey=dbname \
  --set database.postgres.external.sslmode=require
```

> Tip: with the individual-keys form, passwords containing URL-special characters
> can break the assembled DSN. Prefer the `uri` key (CNPG provides one).

<details>
<summary>Example CloudNativePG cluster to pair with this</summary>

```yaml
apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata:
  name: todo-db
spec:
  instances: 1
  storage:
    size: 2Gi
  bootstrap:
    initdb:
      database: todo
      owner: todo
# CNPG then creates Secret "todo-db-app" — reference it as external.existingSecret.
```
</details>

### 4. Expose via Ingress

```sh
helm install todo oci://ghcr.io/inqbeo/charts/todo \
  --set ingress.enabled=true \
  --set ingress.className=nginx \
  --set ingress.hosts[0].host=todo.example.com \
  --set ingress.hosts[0].paths[0].path=/ \
  --set ingress.hosts[0].paths[0].pathType=Prefix
```

### 5. Expose via Gateway API HTTPRoute

```sh
helm install todo oci://ghcr.io/inqbeo/charts/todo \
  --set httpRoute.enabled=true \
  --set httpRoute.parentRefs[0].name=my-gateway \
  --set httpRoute.parentRefs[0].namespace=gateway-system \
  --set httpRoute.hostnames[0]=todo.example.com
```

---

## Authentication

Single user, no roles — logged in means full access. Password precedence:

1. `app.auth.existingSecret` — reference a Secret you manage (key `passwordKey`).
2. `app.auth.password` — set inline; the chart stores it in a generated Secret.
3. Neither — the app generates a random password on first start and logs it once.

The app hashes the password into the database on first start and then **freezes**
it: changing it later has no effect once the database exists. To rotate, change
it at the source and reset the stored credential in the database.

---

## Values

| Key | Default | Description |
|---|---|---|
| `replicaCount` | `1` | App replicas (sessions are in-memory — see note below). |
| `image.repository` | `ghcr.io/inqbeo/containerization-demo-webapp` | Image repo. |
| `image.tag` | `""` | Image tag; empty → chart `appVersion`. |
| `app.companyName` | `Inqbeo` | Name shown in the UI header. |
| `app.theme` | `coral` | UI skin: `coral` \| `navy` \| `dark`. |
| `app.logLevel` | `info` | `debug` \| `info` \| `warn` \| `error`. |
| `app.auth.username` | `admin` | Login username. |
| `app.auth.existingSecret` | `""` | Secret holding the login password. |
| `app.auth.passwordKey` | `password` | Key in that Secret. |
| `app.auth.password` | `""` | Inline password (stored in a generated Secret). |
| `database.type` | `sqlite` | `sqlite` \| `postgres`. |
| `database.sqlite.persistence.enabled` | `true` | Persist the SQLite file on a PVC. |
| `database.sqlite.persistence.size` | `1Gi` | PVC size. |
| `database.sqlite.persistence.existingClaim` | `""` | Use an existing PVC. |
| `database.postgres.mode` | `internal` | `internal` (chart deploys PG) \| `external`. |
| `database.postgres.internal.image` | `postgres:16-alpine` | In-chart Postgres image. |
| `database.postgres.internal.auth.username` | `todo` | PG user. |
| `database.postgres.internal.auth.database` | `todo` | PG database. |
| `database.postgres.internal.auth.existingSecret` | `""` | Use your Secret for the PG password. |
| `database.postgres.internal.persistence.size` | `2Gi` | PG PVC size. |
| `database.postgres.external.existingSecret` | `""` | **Required** for external mode. |
| `database.postgres.external.uriKey` | `uri` | Secret key with the full DSN (CNPG). Set `""` to use individual keys. |
| `database.postgres.external.hostKey/portKey/userKey/passwordKey/dbnameKey` | `host`/`port`/`username`/`password`/`dbname` | Individual-key mapping. |
| `database.postgres.external.sslmode` | `require` | `sslmode` for the assembled DSN. |
| `service.type` | `ClusterIP` | Service type. |
| `service.port` | `80` | Service port (container always listens on 8080). |
| `ingress.enabled` | `false` | Create an Ingress. |
| `httpRoute.enabled` | `false` | Create a Gateway API HTTPRoute. |
| `probes.liveness.enabled` / `probes.readiness.enabled` | `true` | Probe `GET /healthz`. |
| `resources` / `nodeSelector` / `tolerations` / `affinity` | `{}` / `{}` / `[]` / `{}` | Standard scheduling knobs. |

See [`values.yaml`](values.yaml) for the fully commented reference.

> **Note on replicas:** sessions are stored in memory, so with `replicaCount > 1`
> a user may be redirected to the login page when routed to another pod. This is
> intentional course material on why session state gets externalised. For SQLite,
> a single replica with a `ReadWriteOnce` PVC is the supported setup; use Postgres
> for multi-replica.

---

## Test

```sh
helm test todo      # runs a pod that curls /healthz through the Service
```

## Uninstall

```sh
helm uninstall todo
# PVCs are retained by design; delete them explicitly if you want the data gone:
kubectl delete pvc -l app.kubernetes.io/instance=todo
```
