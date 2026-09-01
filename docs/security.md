# Security model

This document covers what Sonde can see, what it cannot, and what a compromise
of each part would get an attacker. It is written to be read before the README
by whoever has to approve Sonde running against production.

**Status:** the CLI in §2 and the probe in §3 are implemented, including all
nine v1 checks, enrolment, mTLS and signed results. The Helm chart in
`charts/sonde-probe` installs the probe with exactly the grant below, and the
image is distroless, non-root and read-only.

---

## 1. The three invariants

1. **Every check is read-only.** No `create`, `update`, `patch`, `delete`,
   `exec` or `scale` is ever issued against your infrastructure. The single
   exception is `SubjectAccessReview`, which is a `create` verb by API design
   and performs no mutation — see §5.
2. **Credentials never leave your network.** The probe runs inside your
   environment, holds every credential, and only ever makes outbound connections
   to the control plane. The control plane never dials in and never stores a
   kubeconfig, cloud key or IdP secret.
3. **Secret values are never read.** `secret_key_present` asserts that a key
   exists. The value is not read into a result, logged, hashed, or transmitted.

## 2. The CLI

`sonde parse` reads Markdown and writes JSON. It executes nothing: no commands,
no template expansion, no network. It is safe to run against untrusted input,
including a pull request from a fork.

`sonde check` executes assertions against whatever infrastructure the flags
point it at, using credentials already on the machine — your kubeconfig, your
resolver. It makes no outbound connection to anything else: the CLI works
standalone, with no account and no control plane.

A runbook can therefore cause reads of the resources it names, and nothing else.
The `command/parses` check is the one worth stating explicitly: it parses the
documented command and looks up the objects the command names. It never runs
the command, including when the command is `kubectl delete`.

Identity credentials are read from the environment — `SONDE_KEYCLOAK_CLIENT_ID`
and `SONDE_KEYCLOAK_CLIENT_SECRET` — and never from a flag, because a client
secret on a command line lands in shell history, in a process list and in CI
logs. The Keycloak service account needs only `view-groups`; no write scope of
any kind is used. A failed token request reports its status and never its
response body, which can echo credentials back.

## 3. The probe

The probe is a Go binary that runs inside your cluster.

- No inbound ports. It long-polls the control plane for work; nothing dials into
  it, including the control plane.
- Enrolment is a one-time token with a 15-minute TTL, usable once. The probe
  generates its keypair locally and the private key never leaves the pod — it is
  not sent, not recoverable from anything the control plane stores, and not
  backed up. A probe that loses its state directory re-enrols as a new probe.
- Client certificates last 24 hours and are renewed with the one they replace. A
  probe that lets its certificate lapse must be enrolled again by a human, which
  is deliberate: a probe that has been off longer than a certificate's lifetime
  should not silently rejoin.
- Results are signed with Ed25519 and hash-chained, so a result cannot be
  altered or dropped from the history without detection — see
  [evidence.md](evidence.md). The probe also compares the control plane's record
  of its history with its own on every lease, and **stops** if they disagree
  rather than writing a chain with a hole in it.
- Image: distroless, non-root, read-only root filesystem, no added capabilities.

### RBAC — the complete grant

```yaml
rules:
  - apiGroups: ["", "apps", "batch", "networking.k8s.io"]
    resources: ["*"]
    verbs: ["get", "list"]
  - apiGroups: ["authorization.k8s.io"]
    resources: ["subjectaccessreviews"]   # only for can_i with as_user/as_group
    verbs: ["create"]
```

No `watch`: nothing in Sonde streams. No mutation verb of any kind.

The second rule is optional. Without it, `can_i` checks that name another
identity cannot run — everything else works — and they report `error`, not
`fail`, because Sonde could not determine the answer.

### What the probe can read

`get` and `list` on those API groups includes Secret objects, which is how
`secret_key_present` enumerates keys. The runner reads the key set and discards
the values immediately; no value reaches a result, a log line, or the network.

If that grant is more than you will give, omit `secret_key_present` from your
runbooks and narrow the first rule's `resources` list to the types your checks
actually name.

## 4. Threat model

**A compromised control plane** can enqueue jobs. Every job is a read-only check
from the catalogue, so what it can achieve is: read the resources your runbooks
already name, and learn resource names. It cannot read secret values, cannot
mutate anything, and cannot obtain a credential — it holds none, and the probe
never sends any.

**A compromised probe** has whatever its ServiceAccount has: read access to the
API groups above, in one cluster. This is why the recommendation is one probe
per cluster rather than one probe holding several kubeconfigs.

**A malicious runbook** — someone with commit access to your docs adding a check
— can cause reads of resources in the environments the probe can already reach,
and can cause outbound HTTP GETs to hosts it names. It cannot cause a write, a
command execution, or a secret value to be transmitted. Review runbook changes
the way you review code; they are executable.

**A compromised customer network** is not something Sonde defends against, and
Sonde does not widen it: the probe opens no listening port.

## 5. Why `SubjectAccessReview` is a `create`

`can_i` is implemented with `authorization.k8s.io/v1`:

- `SelfSubjectAccessReview` asks about the probe's own identity. Every
  ServiceAccount may create these about itself; no special grant is needed.
- `SubjectAccessReview` asks about *another* identity — `as_user`, `as_group` —
  and requires `create` on `subjectaccessreviews`, which is a privileged grant.

Both are submitted as `create` because that is how the Kubernetes API models a
question: the request body is the question, the response body is the answer, and
nothing is persisted. No object is created and nothing in the cluster changes.

The grant is privileged for a different reason: being able to ask "may this user
do this?" about arbitrary users is an authorization oracle. Grant it if you want
`can_i` checks about your on-call groups — which is the check most likely to
catch a runbook that cannot be followed — and withhold it if you do not.

## 6. Reporting a vulnerability

Open a private security advisory on the repository rather than a public issue.
