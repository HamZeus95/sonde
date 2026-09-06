# Sonde — design and contract

> This file is the authoritative context for any AI agent working on this codebase.
> Read it fully before writing code. If a request contradicts the **Invariants** section,
> stop and say so instead of complying.

---

## 1. What Sonde is

**Sonde is a test harness for operational runbooks.**

Ops teams keep 50–500 runbooks in Git, Confluence, or Notion. Those runbooks contain factual
claims about live infrastructure:

- "Scale `deployment/payments-api` in namespace `payments`"
- "Open the Grafana board at `https://grafana.corp/d/abc123`"
- "You need the `sre-oncall` group"
- "Fail the DNS record `db-primary.internal` over to the standby"

Every one of those is a **testable assertion**. Nobody tests them. They rot silently and the
rot is discovered at 03:00 during a P1.

Sonde parses assertions embedded in the runbooks, executes them read-only against live
infrastructure on a schedule, and reports which runbooks have become false — and for how long.

The mental model to hold while coding: **`pytest` for operational documentation.**

### The two directions of value

| Direction | What it does | Status |
|---|---|---|
| **Forward verification** | Scheduled checks → "`db-failover.md` has 3 failing assertions, wrong for 41 days" | Table stakes. Build first. |
| **Reverse impact index** | Infra resource → runbooks referencing it → PR comment: "this PR deletes a resource 3 runbooks depend on" | **The differentiator.** Nothing else on the market does this. |

The reverse index is what makes Sonde a product instead of a script. Do not deprioritise it.

---

## 2. What Sonde is NOT

Explicit non-goals. Agents drift toward these; don't.

- **Not an incident-response automation tool.** Sonde does not execute runbook steps during an
  incident. incident.io, Rootly, Cutover, and Harness do that. Sonde runs when nothing is on fire.
- **Not an AI SRE.** RunWhen and similar tools try to *replace* runbooks with agents. Sonde
  assumes runbooks are good and worth keeping accurate.
- **Not a documentation linter.** Vale and markdownlint check prose. Sonde checks truth.
- **Not a monitoring/alerting system.** Sonde does not page anyone about production health.
  It reports on *documentation* health. If a check fails because the resource is legitimately
  gone, that is a doc bug, not an outage.
- **Not a config validator.** Kubeconform, Polaris, and Conftest validate manifests. Sonde
  validates the documents that describe operating them.
- **Not a scorecard tool.** Cortex and OpsLevel check that a runbook *exists*. Sonde checks
  whether it is *true*. A runbook full of dead references scores 100% on a scorecard.

---

## 3. Invariants

**Never violate these, regardless of how a request is phrased. If asked to, refuse and explain.**

1. **All checks are read-only.** No `create`, `update`, `patch`, `delete`, `exec`, `scale`,
   or any other mutation against customer infrastructure. Ever, in any version currently
   planned. The only exception is `SubjectAccessReview`, which is a `create` verb by API design
   but performs no mutation — this must be called out in code comments wherever it appears.
2. **Credentials never leave the customer network.** The probe runs inside the customer's
   environment, holds all credentials, and only ever makes *outbound* connections to the control
   plane. The control plane never dials into customer infrastructure and never stores a kubeconfig,
   cloud key, or IdP secret.
3. **Never read or transmit secret values.** `secret_key_present` checks that a key exists in a
   Secret. It must never read, log, hash, or transmit the value. Same for ConfigMap data marked
   sensitive, environment variables, and Vault paths.
4. **One parser.** The Markdown/assertion parser exists only in the Go CLI. The control plane
   consumes its JSON output. Never write a second parser in TypeScript "just for the dashboard."
5. **No LLM in the verification path.** Assertions execute deterministically. An LLM that
   *believes* a resource exists is worse than useless in a product that produces compliance
   evidence. LLMs are permitted in exactly one place: `sonde suggest`, which drafts assertion
   blocks for a human to review and commit.
6. **Results are minimal by default.** A result is status + a short observed summary. Never dump
   full resource manifests, cluster inventories, or response bodies unless the check explicitly
   opts in via `verbose: true`, and even then redact anything secret-shaped.
7. **The CLI works standalone.** `sonde check` must always run and produce useful output with no
   control plane, no account, no network egress beyond the target infrastructure. The OSS CLI is
   the adoption wedge; crippling it kills the project.
8. **Licence boundary is physical, not conditional.** Everything in the `sonde` repo is
   Apache-2.0. Everything in `sonde-cloud` is proprietary. Never add a licence check, feature
   flag, or "pro" gate inside the OSS repo.

---

## 4. Repository layout

Two repositories. This split is a licensing decision, not a preference — see §12.

### `sonde` — public, Apache-2.0

```
sonde/
├── cmd/sonde/main.go              # cobra entrypoint
├── internal/
│   ├── parse/                     # Markdown AST → Runbook + []Check
│   │   ├── parser.go
│   │   ├── frontmatter.go
│   │   └── testdata/              # golden files: .md in, .json out
│   ├── model/                     # shared types; NO imports from other internal pkgs
│   │   ├── runbook.go
│   │   ├── check.go
│   │   └── result.go
│   ├── checks/
│   │   ├── registry.go            # kind+check → Runner
│   │   ├── kubernetes/
│   │   ├── http/
│   │   ├── dns/
│   │   ├── identity/
│   │   └── command/
│   ├── report/                    # human, json, junit, tap writers
│   ├── probe/                     # daemon mode: job lease loop, result signing
│   └── canonical/                 # resource URI canonicalisation (see §7)
├── charts/sonde-probe/            # Helm chart, read-only RBAC
├── docs/
│   ├── runbook-contract.md        # the format spec — versioned, breaking changes need a bump
│   └── security.md                # RBAC, threat model, what the probe can and cannot see
├── examples/runbooks/
├── .github/workflows/
└── LICENSE                        # Apache-2.0
```

### `sonde-cloud` — private, proprietary

```
sonde-cloud/
├── apps/
│   ├── api/                       # Hono on Bun
│   │   └── src/routes/{auth,runbooks,checks,results,jobs,webhooks}.ts
│   ├── worker/                    # scheduler: enqueues jobs, computes health scores
│   └── web/                       # React + Vite SPA
├── packages/
│   ├── db/                        # Drizzle schema + migrations
│   └── contracts/                 # zod schemas mirroring Go's JSON output
├── infra/                         # OpenTofu: Hetzner, DNS, object storage
└── compose.yaml
```

**Cross-repo contract:** `packages/contracts` zod schemas must stay in lockstep with the Go
`internal/model` structs. When either changes, update both in the same session and bump
`schema_version`. There is no codegen yet; if this becomes painful, generate JSON Schema from Go
and derive zod from it — do not hand-maintain a third copy.

---

## 5. The Runbook Contract

Assertions live inside the runbook as fenced code blocks. The runbook must remain readable prose.
If a human has to leave the document to maintain its tests, adoption dies — this is the single
most important design constraint in the product.

````markdown
---
sonde:
  version: 1
  id: payments-scale-up
  owner: team-payments
  environment: prod-eu-1
  criticality: high        # high | medium | low
---

# Runbook: Scale the Payments API

## Step 1 — Confirm the deployment exists

```sonde
id: payments-deploy-exists
kind: kubernetes
check: resource_exists
resource: deployment/payments-api
namespace: payments
```

    kubectl -n payments get deploy payments-api

## Step 2 — Confirm on-call can actually do this

```sonde
id: oncall-can-scale
kind: kubernetes
check: can_i
verb: patch
resource: deployments
namespace: payments
as_group: system:sre-oncall
```
````

### Why fenced YAML and not a sidecar file

- Renders as a harmless code block in GitHub, Confluence, Notion, and Backstage TechDocs with no
  plugin. The document still looks fine to someone who has never heard of Sonde.
- The assertion sits next to the step it describes, so it gets updated in the same edit.
- Extractable with a standard Markdown AST walk — no custom lexer.

### Parser rules

- Info string must be exactly `sonde`. Blocks tagged `sonde-ignore` are skipped.
- Block body is YAML. `id`, `kind`, and `check` are required.
- `id` must be unique within a runbook. Duplicate → parse error, non-zero exit.
- A file with no `sonde:` frontmatter key is **not** a Sonde runbook: skip silently, do not warn.
- Unknown `kind`/`check` pairs → parse error listing valid options. Never silently ignore.
- Unknown *fields* within a known check → warning, not error. Forward compatibility matters more
  than strictness here.
- Parsing must never execute anything. `sonde parse` is safe to run on untrusted input.

---

## 6. Check catalogue (v1)

| kind | check | Required fields | Answers |
|---|---|---|---|
| `kubernetes` | `resource_exists` | `resource`, `namespace?` | Does it still exist? |
| `kubernetes` | `field_equals` | `resource`, `path`, `value` | Is the image tag / replica count still what the doc says? |
| `kubernetes` | `field_gte` | `resource`, `path`, `value` | Is `spec.replicas >= 3`? |
| `kubernetes` | `can_i` | `verb`, `resource`, `as_user?`, `as_group?` | **Can the on-call role actually run this command?** |
| `kubernetes` | `secret_key_present` | `resource`, `key` | Does `secret/db-creds` still have key `PGPASSWORD`? (existence only) |
| `http` | `status_is` | `url`, `expect` | Is the dashboard / wiki / status page link alive? |
| `dns` | `record_exists` | `name`, `type`, `expect?` | Does `db-primary.internal` resolve? Is the failover CNAME right? |
| `identity` | `group_exists` | `provider`, `group` | Does the Keycloak group `sre-oncall` still exist? |
| `command` | `parses` | `command` | Does the documented `kubectl`/`aws` invocation still parse, and do its objects exist? |

### `can_i` — the differentiator

No competing tool asks whether the person following the runbook still *has the permission the
runbook assumes*. Implement via `authorization.k8s.io/v1`:

- `SelfSubjectAccessReview` — asks about the probe's own identity. Needs no special grant.
- `SubjectAccessReview` — asks about *another* identity (`as_user`/`as_group`). Requires `create`
  on `subjectaccessreviews`, which is a privileged grant.

Both must be supported. When `as_user`/`as_group` is absent, use the Self variant. Document the
privilege difference prominently in `docs/security.md` — a security reviewer will ask, and having
a crisp answer is worth real credibility.

### Adding a new check kind

1. Define the spec struct in `internal/model`.
2. Implement `Runner` in `internal/checks/<kind>/`.
3. Register in `internal/checks/registry.go`.
4. Add golden parser testdata.
5. Add a fixture-based execution test (envtest for k8s, `httptest` for http, stub resolver for dns).
6. Document it in `docs/runbook-contract.md` and add an example runbook.
7. Add the canonicalisation rule in `internal/canonical/` if the check references a resource.

All seven steps, or the check is not done.

---

## 7. Canonical resource URIs

The reverse index depends entirely on this. Get it right early; changing it later means
reindexing everything.

```
k8s://<cluster>/<namespace>/<kind>/<name>
k8s://prod-eu-1/payments/deployment/payments-api
http://grafana.corp.example/d/abc123          # scheme+host+path, query stripped
dns://db-primary.internal/CNAME
idp://keycloak/prod/group/sre-oncall
aws://<account>/<region>/<service>/<type>/<id>
```

Rules:
- Kind is lowercase singular (`deployment`, not `Deployments`).
- Cluster-scoped resources use the literal namespace `_`.
- Environment maps to cluster when the check omits an explicit cluster.
- Canonicalisation is pure and deterministic: same input → same URI, no network calls.
- Live in `internal/canonical/` so both the CLI and the control plane's PR bot use identical logic.

---

## 8. Architecture

```
   Git / Confluence ───▶ ┌────────────────────────────────────┐
   (runbook sources)     │  Control plane (sonde-cloud)       │
                         │  Hono/Bun API · Postgres · React   │
                         │  Scheduler (pg SKIP LOCKED queue)  │
                         └──────▲──────────────────┬──────────┘
              signed results    │                  │  job leases
              (outbound only)   │                  │  (long poll)
                         ┌──────┴──────────────────▼──────────┐
                         │  Probe (Go binary, in-cluster)     │
                         │  read-only ServiceAccount          │
                         │  no inbound ports, no cred egress  │
                         └──────┬─────────────────────────────┘
                                │ read-only
              ┌─────────────────┼──────────────────┐
              ▼                 ▼                  ▼
        Kubernetes API      HTTP / DNS        Keycloak / Vault
```

**Job lifecycle:** scheduler enqueues per-check jobs → probe long-polls `GET /v1/jobs` →
leases with `FOR UPDATE SKIP LOCKED` → executes → `POST /v1/results` with Ed25519 signature and
`prev_hash` → control plane verifies chain and recomputes health score.

**Cadence:** `criticality: high` hourly, `medium` every 6h, `low` daily. Configurable per runbook.

**Flap suppression:** a check must fail **twice consecutively** before the runbook is marked
broken. A false "your runbook is wrong" is worse than silence — it is the fastest way to lose a
user permanently.

---

## 9. Data model

```sql
create table tenants (
  id uuid primary key default gen_random_uuid(),
  name text not null,
  created_at timestamptz not null default now()
);

create table runbooks (
  id uuid primary key default gen_random_uuid(),
  tenant_id uuid not null references tenants(id),
  source text not null,               -- git | confluence | notion
  source_ref text not null,
  slug text not null,                 -- frontmatter sonde.id
  owner text, environment text, criticality text,
  content_hash text not null,
  updated_at timestamptz not null default now(),
  unique (tenant_id, source, source_ref)
);

create table checks (
  id uuid primary key default gen_random_uuid(),
  runbook_id uuid not null references runbooks(id) on delete cascade,
  local_id text not null,
  kind text not null,
  spec jsonb not null,
  enabled boolean not null default true,
  unique (runbook_id, local_id)
);

-- the moat. give it a real table, not a jsonb column.
create table resource_refs (
  id bigserial primary key,
  tenant_id uuid not null references tenants(id),
  check_id uuid not null references checks(id) on delete cascade,
  runbook_id uuid not null references runbooks(id) on delete cascade,
  provider text not null,             -- k8s | aws | dns | http | idp
  cluster text, namespace text, resource_type text, resource_name text,
  canonical text not null
);
create index on resource_refs (tenant_id, canonical);

create table probes (
  id uuid primary key default gen_random_uuid(),
  tenant_id uuid not null references tenants(id),
  name text not null, environment text not null,
  last_seen_at timestamptz, cert_fingerprint text
);

create table check_runs (
  id bigserial primary key,
  tenant_id uuid not null references tenants(id),
  check_id uuid not null references checks(id) on delete cascade,
  probe_id uuid references probes(id),
  status text not null,               -- pass | fail | error | skipped
  observed jsonb, latency_ms integer,
  ran_at timestamptz not null default now(),
  prev_hash text, self_hash text not null, signature text not null
);
create index on check_runs (check_id, ran_at desc);

create table jobs (
  id bigserial primary key,
  tenant_id uuid not null, probe_id uuid,
  payload jsonb not null,
  run_after timestamptz not null default now(),
  leased_until timestamptz, attempts int not null default 0
);
create index on jobs (probe_id, run_after) where leased_until is null;
```

Dequeue pattern (do not replace this with Redis, BullMQ, or a cron library):

```sql
update jobs
set leased_until = now() + interval '2 minutes', attempts = attempts + 1
where id in (
  select id from jobs
  where probe_id = $1 and run_after <= now()
    and (leased_until is null or leased_until < now())
  order by run_after for update skip locked limit 10
)
returning *;
```

**`status` semantics — do not conflate:**
- `pass` — assertion held.
- `fail` — assertion did not hold. **The runbook is wrong.**
- `error` — Sonde could not determine the answer (probe offline, API timeout, RBAC denied).
  Never counts toward drift. Surfaced separately as probe health.
- `skipped` — disabled, or a dependency check already failed.

Reporting `error` as `fail` would tell users their docs are broken when Sonde is broken. This is
the single most damaging bug class in the product.

**Row-level security.** Every tenant-scoped table (`runbooks`, `checks`, `resource_refs`,
`probes`, `check_runs`, `jobs`) enables RLS as a backstop behind the repository layer, not
instead of it. The app role is `nosuperuser nobypassrls` and does not own the tables; migrations
run as a separate owner role; tables are `force row level security`. The tenant is set per
transaction with `set_config('sonde.tenant_id', $1, true)` — `local = true`, so it cannot leak
across pooled transactions. Policies read it with `current_setting('sonde.tenant_id')::uuid`
**without** `missing_ok`: an unset tenant must raise, not quietly match zero rows, or a
misconfigured request renders as "this tenant has no drift" — the §9 sin at the storage layer.
The scheduler is legitimately cross-tenant and connects as its own small-surface bypass role.

---

## 10. Tech stack and rationale

| Layer | Choice | Why |
|---|---|---|
| CLI + probe | **Go** | `client-go` is the canonical k8s client; single static binary matters when asking people to run it in prod; Go is the language of the EU platform-engineering market |
| Markdown | `goldmark` | AST access, CommonMark-compliant |
| CLI framework | `spf13/cobra` | Standard |
| API | **Hono on Bun** | Fast startup, cheap container, low novelty budget spent |
| DB | **Postgres 16 + Drizzle** | JSONB for specs/evidence; also the job queue |
| Frontend | **React + Vite + TanStack Query + Tailwind** | SPA, not Next.js — API is a separate service, SSR buys nothing |
| Human auth | **Keycloak OIDC**, auth code + PKCE | Tenant claim in token |
| Probe auth | Enrolment token → keypair → **mTLS**, 24h rotation | Probe never holds a long-lived shared secret |
| Evidence | **Ed25519 + hash chain** | Turns a dashboard into a compliance artifact for ~1 day of work |
| Infra | **OpenTofu** + Hetzner + Caddy + Tailscale + SOPS/age | See §14 |

**Do not add without an explicit decision recorded in §16:** Kafka, Redis, microservices, a
Kubernetes operator, gRPC, a monorepo build tool, GraphQL, an ORM other than Drizzle, or any
service mesh. One API, one worker, one database.

**Keep `postgres-js` as a devDependency** — `drizzle-kit migrate` needs it even when the runtime
uses Bun's native SQL driver. This has already cost time once.

---

## 11. Code conventions

### Go

- `gofmt` + `golangci-lint` (errcheck, govet, staticcheck, revive, gosec) clean before commit.
- Errors wrapped with `%w` and context: `fmt.Errorf("parse %s: %w", path, err)`.
- No `panic` outside `main`. No `log.Fatal` in library code.
- `context.Context` first parameter on anything doing I/O. Every check runner takes a context with
  a deadline; default per-check timeout 30s, configurable.
- Interfaces defined by the consumer, not the producer.
- Table-driven tests. Golden files for the parser in `internal/parse/testdata/`.
- `envtest` for Kubernetes check tests — never a live cluster in CI.
- No third-party dependency for anything the stdlib does adequately.

**Exit codes** (contractual — CI depends on them, never change casually):

| Code | Meaning |
|---|---|
| 0 | All checks passed |
| 1 | One or more checks failed (runbook is wrong) |
| 2 | Usage error / bad flags |
| 3 | Parse error in one or more runbooks |
| 4 | Execution error — could not reach target infrastructure |

### TypeScript

- Strict mode. No `any`. Zod-validate every request body and every payload crossing the
  Go↔TS boundary.
- Every DB query filtered by `tenant_id`. This is enforced in a repository layer — no raw Drizzle
  calls in route handlers. A missing tenant filter is a data breach, not a bug.
- No business logic in React components. Server owns health-score computation; the SPA renders.

### Commits and PRs

- Conventional Commits. DCO sign-off (`git commit -s`) on every commit — see §12.
- One logical change per PR. A PR that touches the parser and the dashboard is two PRs.

---

## 12. Licensing and contribution

- **`sonde`: Apache-2.0.** Not BSL, not "Apache now, relicense later." Once external contributors
  land commits you cannot relicense without a CLA or chasing everyone. Decide once, here, now.
- **`sonde-cloud`: proprietary**, private from the first commit. Never a public repo that later
  goes private.
- **DCO, not CLA.** Sign-off line on commits. CLAs measurably suppress drive-by contributions,
  which are the point of open-sourcing the CLI.
- **Trademark the name separately from the code licence.** Apache-2.0 gives away the code, not the
  name. This is what stops someone shipping "Sonde Cloud."

The commercial boundary is *statefulness*, not crippled features. The CLI is stateless: it runs,
prints, exits. Everything paid is inherently stateful — history, trends, cross-repo index, the PR
bot, evidence bundles, SCIM. Nobody feels cheated by that line.

Free vs paid, so an agent never gates the wrong thing:

| Free (OSS) | Paid (cloud) |
|---|---|
| Parser, all check kinds, local execution | Hosted control plane, history, drift trends |
| JUnit/TAP output, GitHub Action | Cross-repo reverse index + PR bot |
| Probe Helm chart, single cluster | Confluence / Notion sync |
| Format spec, docs | Signed evidence export (DORA / ISO 27001 / SOC 2) |
| Plain OIDC login | SAML + SCIM, audit log, self-hosted licence, SLA |

Paywalling basic SSO gets projects publicly mocked. Paywalling SCIM does not.

---

## 13. Security model

Publish `docs/security.md` before asking anyone to install the probe. Security reviewers read it
before the README.

**Probe RBAC — the complete grant, kept minimal:**

```yaml
rules:
  - apiGroups: ["", "apps", "batch", "networking.k8s.io"]
    resources: ["*"]
    verbs: ["get", "list"]
  - apiGroups: ["authorization.k8s.io"]
    resources: ["subjectaccessreviews"]   # required only for can_i with as_user/as_group
    verbs: ["create"]
```

- No `watch` (nothing needs streaming), no `secrets` read beyond key enumeration via `get` on the
  Secret object — and the value is discarded immediately after key presence is determined.
- Probe image: distroless, non-root, read-only root filesystem, no capabilities.
- Enrolment: one-time token, 15-minute TTL, single use. Probe generates the keypair locally; the
  private key never leaves the pod.
- Threat model to document: what a compromised control plane can do (answer: enqueue read-only
  jobs, learn resource names — not read secrets, not mutate anything).

---

## 14. Deployment

Two servers. Hetzner **CX/CAX line only** — the June 2026 price adjustment made CPX and CCX poor
value (CPX rose ~2.4–2.75x, CCX ~2.1–2.7x, while CX/CAX rose only ~1.3–1.4x).

- **Server A — control plane, always on.** CX33-class (4 vCPU / 8 GB / 80 GB), ~€8.50/mo.
  Debian 13, Docker + Compose v2, Caddy (automatic TLS), Tailscale with **port 22 closed to the
  public internet** — only 80/443 exposed. Secrets via SOPS+age, encrypted in-repo, decrypted at
  deploy. Deploy = GitHub Actions → GHCR → SSH `docker compose pull && up -d`.
- **Server B — demo/dogfood cluster, ephemeral.** Same class, `tofu apply` → single-node k3s +
  ArgoCD + deliberately-drifty demo apps + probe, `tofu destroy` when done. ~€0.30/mo at 20h use.

Backups (the part that actually matters): nightly `pg_dump` → restic → **a different provider than
compute**. Weekly snapshot. **Monthly restore test.** Write the restore procedure as a Sonde
runbook and let Sonde verify it — that keeps the dogfood loop alive.

Observability: start on Grafana Cloud free tier + Alloy. Self-hosting the LGTM stack on an 8 GB
box wastes RAM on retention nobody reads; move it to Server B later.

---

## 15. Build phases

Each phase has a **definition of done**. Do not start the next phase until the previous one's DoD
is met and demonstrated.

### Phase 0 — Discovery (1 week, zero code)
15 conversations with SREs/platform engineers. **Gate: ≥8 report a concrete wrong-runbook
incident, AND ≥5 react positively to the PR-bot idea.** Below that, stop the project.

### Phase 1 — OSS CLI (3 weeks)
1. `sonde parse ./runbooks` → JSON. **No execution.** Get the parser boring and correct first.
2. `sonde check --kubeconfig ...` executing `resource_exists`.
3. Add `http`, `dns`, `can_i`.
4. `--format junit|tap|json|human`.
5. GitHub Action wrapper.

**DoD:** delete the demo deployment, run `sonde check`, watch it go red, and confirm the exit code
fails a CI job. Then dogfood: 5 real runbooks for your own infra, nightly Action.

### Phase 2 — Control plane + probe (4 weeks)
Enrolment → mTLS → long-poll → signed results → hash chain → scheduler.

**DoD:** probe in a real cluster, results arriving with valid signatures and an unbroken chain.
Kill the probe for an hour; jobs re-lease cleanly without duplication.

### Phase 3 — Dashboard + reverse index (3 weeks)
Runbook list ranked by health, drift timeline, "**wrong for N days**" as the headline metric.
GitHub App: diff manifests/Terraform plan → canonicalise removed resources → query
`resource_refs` → comment on the PR.

**DoD:** open a PR deleting a resource, get the bot comment naming affected runbooks. **Record a
60-second screen capture.** That clip is the README GIF, the launch post, and the interview exhibit.

### Phase 4 — Adoption reducers (4 weeks)
`sonde suggest` (LLM drafts assertions from prose, human approves the diff), Confluence/Notion
read connectors, Slack weekly digest.

Authoring friction is the risk most likely to kill this product. Attack it directly.

### Phase 5 — Monetisable surface
Signed evidence export mapped to DORA Art. 11 (documented, tested ICT response and recovery
procedures), ISO 27001 A.5.24–5.26, SOC 2 CC7.3–CC7.4. SSO, audit log, self-hosted licence,
billing via a merchant of record.

**Build the dashboard last. Everyone builds the dashboard first.**

---

## 16. Decision log

Closed decisions. Reopening one requires a written reason appended here, not a silent change.

| # | Decision | Rationale |
|---|---|---|
| 1 | Go for CLI/probe, not TypeScript | `client-go`; static binary; EU platform-eng job market. Costs ~2 weeks vs TS. Accepted. |
| 2 | Assertions in fenced blocks, not sidecar files | Doc stays readable and renders fine everywhere; assertion updated in the same edit as the step |
| 3 | Postgres as job queue, not Redis | One less service to run and explain; SKIP LOCKED handles far more throughput than this will ever see |
| 4 | Apache-2.0 from day one, no BSL | Adoption is the risk, not cloud providers reselling it. Relicensing later is blocked by contributors. |
| 5 | Read-only forever in v1/v2 | Removes ~80% of security-review friction |
| 6 | SPA, not Next.js | API is a separate Hono service; SSR adds a deployment surface for no gain |
| 7 | Ephemeral demo cluster over persistent homelab | Reproducible, portable across countries, ~€0.30/mo |
| 8 | Canonical HTTP URIs always use the `http` scheme; `https` folds onto it | In a canonical URI the scheme names the *provider* (matching `resource_refs.provider`), not the wire protocol. A dashboard linked as `https://` in one runbook and `http://` in another is one resource, and the PR bot must match it either way. |
| 9 | The check catalogue lives in `internal/model`, not in `internal/checks/registry.go` | `sonde parse` must reject an unknown kind+check pair without linking a Kubernetes client into a binary that runs on untrusted input. The registry stays the authority on what this build can *execute*. |
| 10 | `can_i` and `command/parses` produce no canonical URI | `can_i` names a resource *type*, not an object; `parses` resolves its objects at execution time. Indexing either needs a URI form §7 does not define. Reopen when the PR bot needs RBAC-level impact. |
| 11 | `identity/group_exists` takes an optional `realm`, defaulting to the runbook's `environment` | §7's `idp://keycloak/prod/group/sre-oncall` has a realm segment that §6's field list does not supply. Mirrors how `cluster` falls back to `environment`. |
| 12 | RLS on every tenant-scoped table, behind the repository layer | Verified on Postgres 16.15: without it a forgotten `where tenant_id` returns other tenants' rows; with it the same query is scoped and a cross-tenant insert is rejected. Strict `current_setting` so an unset tenant raises instead of returning an empty result that reads as "no drift". |
| 13 | A host or DNS name that does not resolve is a **fail**; every other transport failure is an **error** | NXDOMAIN on a documented host is the rot the product exists to catch — the dashboard was decommissioned and the link outlived it. A refused connection, a timeout or a TLS failure is a service being down, and reporting that as "your runbook is wrong" would make Sonde a bad monitoring system instead of a good documentation one. Flap suppression covers the transient NXDOMAIN case. |
| 14 | `command/parses` verifies kubectl invocations; anything else is an **error** | Verifying an `aws` invocation needs an AWS client that v1 does not have. A pass that verified nothing is worse than an honest "cannot determine". |
| 15 | A kubernetes check runs against the kubeconfig **context** matching its cluster name, or reports error | Running a runbook's `prod-eu-1` checks against whichever cluster is current produces results that look authoritative and describe the wrong cluster. `--context` is the escape hatch for the single-cluster case. |
| 16 | `can_i` resolves the resource's API group through discovery | Found by the k3d demo: a SubjectAccessReview for `deployments` with an empty API group asks about a core-group resource that does not exist, and the API answers no — which rendered as "your on-call group cannot do this" when it could. A false accusation is the failure mode the product cannot afford, so an unresolvable type is an error, never a denial. Regression test in `internal/checks/kubernetes/client_test.go`. |
| 17 | `can_i` takes an optional `subresource` field rather than kubectl's `pods/exec` spelling | Closes the open question below. `resource` carries `type/name` everywhere else in the catalogue, so reusing the slash would turn a copied-pattern mistake (`resource: deployment/payments-api`) into a review of a subresource named `payments-api`, which the API denies — a false accusation, the failure mode of decision 16 all over again. An optional field is additive and needs no format version bump. |
| 18 | mTLS for probes is terminated by Caddy, not by the API | Bun exposes no peer certificate to a request handler: neither `Bun.serve` nor its `node:https` shim implements `getPeerCertificate` (verified against Bun 1.3.14), so the application cannot see who presented what. Caddy already fronts the API in §14, and it forwards the certificate's SHA-256 fingerprint — which is exactly what `probes.cert_fingerprint` stores. The API therefore trusts a header, sound only because the API is unpublished and the proxy proves itself with a shared secret. Both conditions are load-bearing and documented where they are relied on. |
| 19 | Issued certificates take the CA's subject as raw DER, never as a string | Re-encoding `CN=sonde probe ca` produces a PrintableString where OpenSSL wrote a UTF8String. Go builds a chain by matching the leaf's issuer bytes against a root's subject bytes exactly, so the two spellings do not chain: the signature verifies and the chain does not, and every handshake fails with "certificate signed by unknown authority". Cost an hour to find; regression test in `apps/api/src/ca.test.ts`. |
| 20 | Three columns added to §9: `probes.public_key`, `enrolment_tokens`, `jobs.check_id` | The first is required to verify any signature at all — without it the chain is decoration. The second is where one-time tokens live; putting them on `probes` would mean a probe row existing before a probe does. The third lets the scheduler tell "already queued" from "queue it" with an index scan rather than a jsonb probe, and makes deleting a check take its pending work with it. A probe's reported build version is logged at enrolment and deliberately **not** stored, pending a decision. |
| 21 | jsonb columns use a custom Drizzle type that does not stringify | Drizzle's built-in `jsonb()` stringifies before handing the value to the driver, and Bun's SQL driver then encodes that string as a JSON scalar. An object round-trips as a quoted string, `jsonb_typeof` says "string", and the probe decodes a job payload into an empty check — three layers away from the cause. Bun's driver encodes objects correctly on its own. |
| 22 | The scheduler assigns work to the most recently active probe, and moves queued work off probes that have gone quiet | Ordering by enrolment date — the obvious choice — sends every job to whichever probe enrolled first, including one that enrolled once and died. The queue then stops, and the affected checks silently stop being verified: the one outcome this product must never produce quietly. |
| 23 | Plan-JSON mapping starts with three families: the Kubernetes provider, Route 53 and Cloudflare records, and anything carrying an ARN | Answers the open question below. Kubernetes because that is what runbooks name most; DNS records because a failover runbook is mostly DNS; ARNs because one already carries every segment an `aws://` URI needs, which generalises to any AWS resource without a per-type table. A deletion that maps to nothing is reported as unmappable rather than dropped — a gap in the index the bot can state out loud. |
| 24 | `sonde impact` lives in the OSS CLI and the PR bot invokes it | §7 requires the CLI and the bot to canonicalise identically, and the only way to guarantee that is one implementation. The bot shells out to the binary, which ships in the control plane's image, the same arrangement as the parser whose JSON the control plane consumes rather than reproducing. |
| 25 | A delete-then-create in a plan is not a removal | The resource is still there when the plan finishes. Warning about it would be noise, and a bot that cries wolf on every replacement gets muted — after which it cannot warn about anything. |

| 26 | A document may declare its metadata in a `sonde-runbook` block instead of frontmatter | Confluence and Notion pages have nowhere to put a YAML header. The alternative was for each connector to invent an id from a page title someone will later rename, which puts identity in the connector rather than in the document. Additive, so the format version stays 1; frontmatter wins when a document has both, and two blocks is an error rather than a guess. |
| 27 | `sonde suggest` drafts through a `Drafter` interface, and everything it returns is re-parsed before a human sees it | Keeps the one permitted use of a model (§3.5) behind a seam: the model drafts, the real parser judges, and the tests use a fake so the suite never calls an API. A block that would not parse is reported as rejected, with the reason, rather than shown as a suggestion. |

| 28 | Wiki connectors convert to Markdown and shell out to `sonde parse` | One parser (§3.4). A connector that decided for itself what a runbook was would be a second one, and the two would disagree about a customer's page within a month. The conversion is shallow on purpose — headings, paragraphs, lists, code — because the parser reads none of the prose; only the block bodies must be exact. |
| 29 | Confluence syncs by polling, not by webhook | Closes the open question below. Confluence Cloud webhooks need an app or an admin-installed automation, which puts a setup step in front of the first sync; polling a space needs a read-scoped API token and nothing else. Drift moves on the scale of hours, so a poll on the scheduler's cadence loses nothing. Revisit if a customer wants sub-minute freshness. |
| 30 | The weekly digest is sent only when something changed or something is still wrong | A message that arrives every week saying "everything is fine" trains people to scroll past it, and the week it says otherwise they scroll past that too. The headline is the longest-standing drift rather than the newest, because the new break gets noticed on its own. |

| 31 | Exporting an evidence bundle is a paid feature; verifying one is free and open source | Evidence only the vendor can check is not evidence. `sonde verify` needs no account and no network, so the party being audited cannot influence the result. This does not move the commercial boundary — the boundary is statefulness, and a verifier runs, prints and exits. |
| 32 | An evidence bundle's runs are ordered by insertion id, never by `ran_at` | A probe executes a batch concurrently, so the instant a check started does not match the order the probe signed them in. Ordering by time produced a bundle whose every signature verified and whose chain did not — the worst kind of bug, because it looks like tampering. |
| 33 | The control mapping states what each control is *not* evidenced by | Claiming a bundle satisfies DORA Art. 11 would be overreach: Sonde shows the documented procedures were verified, not that they are adequate or were rehearsed. An auditor who finds an undisclosed limit stops trusting everything else in the bundle. |
| 34 | RLS policies are applied to every table with a `tenant_id`, discovered rather than listed, and the migration fails if one lacks a policy | Found by adding `audit_events`: the hard-coded list did not include it, so every tenant's audit log was readable by every other. A list is a thing people forget to add to; a query is not. |
| 35 | `audit_events` and `GET /v1/audit` added to §9 and §4 | An audit log is listed as a paid feature in §12 and had nowhere to live. It records what the control plane did — enrolments, renewals, exports — which is a different question from what the checks found, and an auditor asks both. |

| 36 | One probe per cluster, not one probe with several kubeconfigs | Closes the open question below, and everything already assumes it: the chart runs a StatefulSet with one identity and one chain, `probe.environment` is required, and the scheduler assigns work by environment. A probe holding several kubeconfigs would be one compromise away from several clusters, and the blast radius is the whole argument. |
| 37 | `jobs.tenant_id` gets the foreign key the other tables have | Closes the other open question. Nothing was gained by the omission — a job for a tenant that does not exist is unrunnable — and the constraint says so at write time rather than at read time. |
| 38 | A probe with no kubeconfig uses its in-cluster credentials for any cluster name | Found by running the chart: a pod has no kubeconfig, so the strict context matching from decision 15 failed every kubernetes check with "no kubeconfig context named prod-eu-1" — in the deployment the chart exists for. A pod is in exactly one cluster and the operator declared which environment it covers at install, so there is nothing to disambiguate. |
| 39 | Probes are ranked by the most recent evidence they exist, not by `last_seen_at` alone | A probe that has never reported ranked below one that reported an hour ago, so replacing a probe left its queue assigned to the old row and its checks silently unverified. Enrolling a minute ago is evidence of life; not having finished a first batch is not. Unleased work now also moves to the current probe immediately rather than waiting for the old one to age out. |

| 40 | A batch whose answer was lost is not a diverged chain | The probe records the tail of a batch before sending it. A submission that is stored and whose acknowledgement never arrives — a dropped connection, a proxy timeout, a pod evicted between the write and the response — leaves the control plane exactly one batch ahead, which was indistinguishable from a rewritten history and stopped the probe until a human restarted it. A stopped probe means checks silently not being verified, which is the outcome §9 exists to prevent, and it should not take a page to recover from one dropped TCP connection. A tail the probe never wrote still stops it. |

Open questions (answer before the phase that needs them):
- None open.

---

## 17. Working agreements for AI agents

- **Ask before inventing a check kind, a DB column, or a route.** The check catalogue and schema
  are product decisions, not implementation details.
- **Never weaken §3.** If a task seems to require mutation, secret reads, or an LLM in the
  verification path, stop and flag it. There is always another design.
- **Prefer deleting code to adding abstraction.** This is a solo project; the enemy is surface area.
- **When adding a check, do all seven steps in §6.** Half a check is worse than none.
- **Update `docs/runbook-contract.md` in the same commit** as any parser change. The spec is the
  product's public API.
- **No new dependency without justifying it in the PR description.**
- **Never commit anything that would fail `golangci-lint` or `tsc --noEmit`.**
- **Do not generate marketing copy, landing pages, or blog posts** unless explicitly asked. Ship
  the tool.

---

## 18. Glossary

- **Runbook** — a Markdown document describing an operational procedure, containing `sonde` blocks.
- **Check / assertion** — one testable claim inside a runbook.
- **Drift** — the state of an assertion being false. **Drift days** = time since it first failed.
- **Health score** — passing checks ÷ total enabled checks for a runbook, weighted by criticality.
- **Probe** — the Go binary running inside customer infrastructure, executing checks.
- **Control plane** — hosted service: scheduling, storage, dashboard, index, evidence.
- **Reverse index** — `resource_refs`: infra resource → runbooks referencing it.
- **Evidence bundle** — signed, hash-chained export of check history for auditors.
