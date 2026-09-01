# The runbook contract

Format version: **1**
Applies to: `sonde parse`, `sonde check`, and the JSON they emit (`schema_version: 1`).

This document is the product's public API. A runbook written against it must
keep parsing for the life of format version 1.

---

## 1. What makes a file a runbook

A runbook is a Markdown file whose YAML frontmatter contains a `sonde:` key.

```markdown
---
sonde:
  version: 1
  id: payments-scale-up
  owner: team-payments
  environment: prod-eu-1
  criticality: high
---
```

A Markdown file without that key is **not** a runbook. Sonde skips it silently:
no output, no warning, no exit code. Pointing `sonde parse` at a documentation
tree of a thousand files is expected usage.

### Sources with no frontmatter

A Confluence or Notion page is not a file, and there is nowhere to put a YAML
header. Such a document declares itself with a fenced `sonde-runbook` block
carrying the same fields, without the `sonde:` nesting:

````markdown
```sonde-runbook
version: 1
id: db-failover
owner: team-platform
environment: prod-eu-1
criticality: high
```
````

Frontmatter and the block are equivalent, and a document needs one of them. A
document with both uses the frontmatter and warns; a document with two
`sonde-runbook` blocks is an error, because there is no way to tell which one
was meant.

Markdown files in a repository should keep using frontmatter: it stays out of
the rendered page.

| Field | Required | Meaning |
|---|---|---|
| `version` | yes | Format version. Must be `1`. |
| `id` | yes | Stable identifier, unique across the runbooks you scan. |
| `owner` | no | Team that owns the procedure. Carried through to results. |
| `environment` | no | Names the cluster and IdP realm a check runs against unless the check overrides it. |
| `criticality` | no | `high`, `medium` or `low`. Drives re-check cadence in the control plane: hourly, six-hourly, daily. |

Unknown keys under `sonde:` produce a warning, not an error. Keys outside
`sonde:` — your docs tooling's `title`, `tags`, whatever else — are ignored
entirely.

## 2. Assertions

An assertion is a fenced code block whose info string is exactly `sonde`. The
body is YAML.

````markdown
```sonde
id: payments-deploy-exists
kind: kubernetes
check: resource_exists
resource: deployment/payments-api
namespace: payments
```
````

The info string is case sensitive. A block tagged ` ```Sonde ` is an error
rather than a silent skip, because a silently skipped assertion looks exactly
like a passing one.

A block tagged `sonde-ignore` is skipped deliberately. Use it for examples in
documentation about Sonde itself.

### Why the assertion lives in the document

It renders as an ordinary code block in GitHub, Confluence, Notion and Backstage
TechDocs with no plugin, so the document still reads correctly to someone who
has never heard of Sonde. And the assertion sits beside the step it describes,
so it is updated in the same edit — which is the only reason to expect it to
stay true.

## 3. Fields every block accepts

| Field | Type | Default | Meaning |
|---|---|---|---|
| `id` | string | — | Required. Unique within the runbook. Letters, digits, `.`, `-`, `_`; must start with a letter or digit. |
| `kind` | string | — | Required. One of `kubernetes`, `http`, `dns`, `identity`, `command`. |
| `check` | string | — | Required. See the catalogue below. |
| `enabled` | bool | `true` | `false` keeps the assertion in the document as documentation but does not execute it. |
| `timeout` | duration | `30s` | Per-check deadline, as a Go duration string: `5s`, `1m30s`. |
| `verbose` | bool | `false` | Opts this check into a fuller observed summary. Results are minimal by default. |

## 4. The v1 catalogue

### `kubernetes`

`resource` is a `type/name` pair — `deployment/payments-api` — mirroring the
kubectl command usually printed beside it. Plural and short forms (`deployments`,
`deploy`, `deployment.apps`) are accepted and normalised.

`namespace` may be omitted for cluster-scoped objects. `cluster` may be set to
override the runbook's `environment`.

| `check` | Fields | Asks |
|---|---|---|
| `resource_exists` | `resource`, `namespace?`, `cluster?` | Does the object still exist? |
| `field_equals` | `resource`, `path`, `value`, `namespace?`, `cluster?` | Is the field still what the document claims? |
| `field_gte` | `resource`, `path`, `value`, `namespace?`, `cluster?` | Is the numeric field at least this? |
| `can_i` | `verb`, `resource`, `namespace?`, `subresource?`, `name?`, `as_user?`, `as_group?`, `cluster?` | Can this identity still do it? |
| `secret_key_present` | `resource`, `key`, `namespace?`, `cluster?` | Does the Secret still carry this key? |

`path` is a dotted field path with numeric indices for list elements:
`spec.template.spec.containers[0].image`.

`value` in `field_equals` is compared with its JSON type intact: the string
`"3"` does not equal the number `3`.

`can_i` is the odd one out: its `resource` is a resource *type* (`deployments`),
because the Kubernetes authorization API reasons about types. Name a single
object with the optional `name` field, and a subresource with `subresource`:

```yaml
id: oncall-can-exec
kind: kubernetes
check: can_i
verb: create
resource: pods
subresource: exec
namespace: data
as_group: system:sre-oncall
```

kubectl spells that `pods/exec`, but here it is a field of its own, because
`resource` carries `type/name` everywhere else in the catalogue. A runbook
writing `deployment/payments-api` in a `can_i` would otherwise be asking about a
subresource named `payments-api`, and the API's flat "no" would read as "your
on-call group cannot do this" when in fact it can.

Setting `as_user` or `as_group` asks about **another** identity, which requires
the privileged `SubjectAccessReview` grant. Leaving both unset asks about the
probe's own identity and needs no special permission. See
[security.md](security.md). At most one of the two may be set.

`secret_key_present` asserts that a key exists. The value is never read into a
result, logged, hashed or transmitted.

### `http`

| `check` | Fields | Asks |
|---|---|---|
| `status_is` | `url`, `expect?` (default `200`), `method?` (`GET` or `HEAD`) | Is the documented link alive? |

No other method is accepted: checks are read-only, and a runbook check that
POSTs is not a check.

### `dns`

| `check` | Fields | Asks |
|---|---|---|
| `record_exists` | `name`, `type`, `expect?` | Does the name resolve, and to what? |

`type` is one of `A`, `AAAA`, `CNAME`, `MX`, `NS`, `TXT`, `SRV`. Omitting
`expect` asserts existence only.

### `identity`

| `check` | Fields | Asks |
|---|---|---|
| `group_exists` | `provider`, `group`, `realm?` | Does the group the runbook requires still exist? |

`realm` defaults to the runbook's `environment`.

### `command`

| `check` | Fields | Asks |
|---|---|---|
| `parses` | `command`, `namespace?`, `cluster?` | Does the documented command still parse, and do its objects exist? |

The command is **never executed**. It is parsed, and the objects it names are
looked up read-only — including when the command itself is a mutating one.

## 5. Parser rules

- `id`, `kind` and `check` are required in every block.
- A duplicate `id` within one runbook is an error.
- An unknown `kind` or an unknown `check` for a known kind is an error, and the
  message lists the valid options.
- An unknown **field** inside a known check is a warning, not an error: a runbook
  written against a newer Sonde must still parse on an older one.
- Every problem in a file is reported, not just the first.
- Parsing executes nothing — no commands, no template expansion, no network. It
  is safe to run on untrusted input.

## 6. Canonical resource URIs

Every check that names one concrete resource is indexed under a canonical URI.
This is what lets the control plane answer the reverse question: which runbooks
reference the resource this pull request deletes?

```
k8s://<cluster>/<namespace>/<kind>/<name>     k8s://prod-eu-1/payments/deployment/payments-api
http://<host><path>                           http://grafana.corp.example/d/abc123
dns://<name>/<TYPE>                           dns://db-primary.internal/CNAME
idp://<provider>/<realm>/group/<name>         idp://keycloak/prod-eu-1/group/sre-oncall
aws://<account>/<region>/<service>/<type>/<id>
```

Normalisation rules:

- Kubernetes kinds are lowercase and singular; short names and API group
  suffixes are resolved (`deploy`, `deployments`, `Deployment.apps` → `deployment`).
- Cluster-scoped resources use the literal namespace `_`.
- The cluster comes from the check's `cluster`, else the runbook's `environment`.
  With neither, the check still runs but is not indexed, and says so as a warning.
- HTTP URIs always use the `http` scheme. In a canonical URI the scheme names the
  *provider*, the way `k8s://` and `dns://` do, not the wire protocol — so a
  dashboard linked as `https://` in one runbook and `http://` in another is one
  resource. Query strings, fragments and default ports are dropped.
- DNS names are lowercased with the root dot removed; record types are uppercased.

`can_i` and `parses` produce no URI: the first names a resource type rather than
an object, and the second resolves its objects at execution time.

## 7. What counts as a failure

A check has three possible answers, and the difference between the last two is
the point of the product:

- **pass** — the assertion held.
- **fail** — the assertion did not hold. **The runbook is wrong.**
- **error** — Sonde could not determine the answer. The runbook is not accused
  of anything, and this never counts as drift.

Where each check draws that line:

| Situation | Result |
|---|---|
| Object absent (`resource_exists`, `field_equals`, `field_gte`, `secret_key_present`) | fail |
| Field absent, or holding a different value | fail |
| `can_i` answered "no" by the API | fail |
| Secret exists but the key is gone | fail |
| HTTP status other than `expect` | fail |
| HTTP host does not resolve (NXDOMAIN) | fail — the documented host is gone |
| HTTP connection refused, timeout, TLS failure | **error** — the service may be down, which is not a documentation problem |
| DNS name does not resolve | fail |
| DNS resolver failure (SERVFAIL, timeout) | **error** |
| Identity group absent from the realm | fail |
| Identity realm absent | **error** — nothing was learned about the group |
| `command/parses` names an object that is gone | fail |
| `command/parses` on a non-kubectl command | **error** — this build cannot verify it |
| API server unreachable, RBAC denies the read | **error** |
| Resource type the cluster does not know | **error** |
| `field_gte` on a field that is not a number | **error** — the comparison has no answer |
| The check is disabled | skipped |
| No runner for this check in this build | **error** |

Sonde is not a monitoring system. A service being down is an error, not a
failing runbook; a resource being *gone* is a failing runbook.

## 8. Which cluster a check runs against

A kubernetes check runs against the cluster named by its `cluster` field, or by
the runbook's `environment`. The CLI maps that name to a kubeconfig **context**
of the same name.

If no context has that name, the checks report `error` rather than running
against whichever cluster happens to be current: results that look authoritative
and describe the wrong cluster are worse than no results. Pass `--context` to
run every kubernetes check against one context whatever the runbook names, which
is the usual single-cluster case.

A runbook with neither `cluster` nor `environment` uses the current context.

## 9. Exit codes

| Code | Meaning |
|---|---|
| 0 | All checks passed |
| 1 | One or more checks failed — the runbook is wrong |
| 2 | Usage error |
| 3 | Parse error in one or more runbooks |
| 4 | Execution error — could not reach the target infrastructure |

Exit code 4 is deliberately not 1. A check Sonde could not evaluate is not a
runbook that is wrong, and a CI job should be able to tell those apart. When a
run contains both, the exit code is 1: the runbook is wrong whether or not
something else was also unreachable.

Of the output formats, JUnit is the one that keeps the distinction — `<failure>`
against `<error>` — and every CI system already knows what to do with it. TAP
has no way to express it and marks both `not ok`, with `severity:` in the
diagnostic; read the exit code or the JSON if you need to tell them apart.

## 10. Changing this document

Adding an optional field or a new `kind`/`check` pair is backward compatible and
does not change the format version.

Removing a field, changing a default, or changing a canonical URI form is
breaking: it needs a format version bump, and the URI change additionally
invalidates every stored index entry.
