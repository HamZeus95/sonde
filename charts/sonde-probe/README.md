# sonde-probe

Runs the [Sonde](https://github.com/HamZeus95/sonde) probe in a cluster.

The probe executes the assertions in your runbooks read-only and returns signed
results to a control plane. **It listens on no port**, makes only outbound
connections, and no credential it holds is ever sent anywhere — so there is no
Service, no Ingress, and no ingress rule to write.

## The image

Published to `ghcr.io/hamzeus95/sonde` on every release tag, multi-arch
(amd64 and arm64), signed with cosign and carrying an SBOM. The chart pulls the
tag matching its `appVersion`, so a chart version always has an image.

```bash
cosign verify ghcr.io/hamzeus95/sonde:0.1.0 \
  --certificate-identity-regexp 'https://github.com/HamZeus95/sonde/.github/workflows/release.yml@.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

Before the first release exists, build and load it yourself:

```bash
docker build -t sonde-probe:dev .
k3d image import sonde-probe:dev -c <cluster>
helm install ... --set image.repository=sonde-probe --set image.tag=dev --set image.pullPolicy=Never
```

## Install

```bash
helm install sonde-probe ./charts/sonde-probe \
  --namespace sonde --create-namespace \
  --set controlPlane.url=https://sonde.example.com \
  --set probe.environment=prod-eu-1 \
  --set enrolment.existingSecret=sonde-enrolment
```

`probe.environment` is not a label. It is the cluster name a runbook's checks
resolve against, so it has to match the `environment` in the frontmatter of the
runbooks this probe should run. A probe with the wrong one silently runs
nothing.

The enrolment token is single use and expires in fifteen minutes. Prefer
`enrolment.existingSecret` over `enrolment.token`: values passed to Helm end up
in the release history.

```bash
kubectl -n sonde create secret generic sonde-enrolment --from-literal=token=<token>
```

## What it is granted

```yaml
- apiGroups: ["", "apps", "batch", "networking.k8s.io"]
  resources: ["*"]
  verbs: ["get", "list"]
```

That is the whole of it by default. No `watch` — nothing in Sonde streams — and
no verb that changes anything.

Narrow `rbac.resources` to the types your runbooks actually name if you want to
give less. `rbac.apiGroups` and `rbac.resources` are both values.

One optional grant, off by default:

```yaml
rbac:
  subjectAccessReview: true
```

`can_i` checks that name another identity (`as_user`, `as_group`) need `create`
on `subjectaccessreviews`. It is a privileged grant — being able to ask "may
this user do this?" about arbitrary users is an authorization oracle — so you
opt in. Without it those checks report `error`, never `fail`: Sonde says it
could not determine the answer rather than accusing your runbook.

`create` on a SubjectAccessReview is not a mutation. It is how the Kubernetes
API models a question: the request body is the question, the response body is
the answer, and nothing is persisted.

## Why a StatefulSet

The probe has an identity. Its signing key, its client certificate and its place
in the evidence chain live in `/var/lib/sonde`, and an enrolment token is single
use — a probe that loses that directory cannot re-enrol itself and needs a human
with a new token.

`persistence.enabled=false` is for testing only, and the chart says so on
install.

## Hardening

Non-root, read-only root filesystem, all capabilities dropped, no privilege
escalation, `RuntimeDefault` seccomp. The image is distroless: one static binary
and a CA bundle, with no shell and no package manager.

## Values

| Key | Default | |
|---|---|---|
| `controlPlane.url` | — | **Required.** |
| `controlPlane.caCert` / `existingCaSecret` | — | For a control plane with a private CA. |
| `probe.environment` | — | **Required.** Must match your runbooks' `environment`. |
| `probe.pollWait` | `30s` | How long each long poll may block. |
| `probe.concurrency` | `8` | Leased checks run at once. |
| `probe.allowChainReset` | `false` | Leave it. See below. |
| `enrolment.existingSecret` | — | Preferred over `enrolment.token`. |
| `rbac.subjectAccessReview` | `false` | Opt in for `can_i` about other identities. |
| `persistence.enabled` | `true` | Disabling it loses the probe's identity on restart. |
| `hostAliases` | `[]` | For a control plane whose name this cluster's DNS does not know. |

### Reaching a control plane the cluster cannot resolve

A probe in a local cluster often has to reach a control plane running on the
host, by a name the cluster's own DNS knows nothing about. Map it rather than
pointing the probe at a bare IP, so the certificate still verifies:

```yaml
hostAliases:
  - ip: 172.20.0.1            # docker network inspect k3d-<cluster> --format '{{ "{{" }}range .IPAM.Config{{ "}}" }}{{ "{{" }}.Gateway{{ "}}" }}{{ "{{" }}end{{ "}}" }}'
    hostnames: ["host.k3d.internal"]
```

### Changing a value while the probe is crash-looping

A StatefulSet will not roll a pod that never becomes Ready, so a probe stuck on
a diverged chain keeps its old arguments however many times you `helm upgrade`.
Delete the pod to apply the change:

```bash
kubectl -n sonde delete pod sonde-probe-0
```

### `allowChainReset`

The probe compares the control plane's record of its history with its own on
every lease, and stops when they disagree. That is either tampering or a control
plane restored from a backup, and both want a human.

Setting this to `true` says "it was the backup, and I know". It is the only way
the evidence chain loses continuity without someone deciding to, which is why it
is a flag and not a retry.
