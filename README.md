# Sonde

**A test harness for operational runbooks.**

Your runbooks make factual claims about live infrastructure: that
`deployment/payments-api` exists, that a Grafana link resolves, that the
`sre-oncall` group can scale a service. Nobody tests those claims. They rot
quietly, and the rot is discovered at 03:00 during a P1.

Sonde parses assertions embedded in your runbooks, executes them read-only
against live infrastructure, and reports which runbooks have stopped being true.

Think `pytest`, for operational documentation.

---

## What it looks like

An assertion lives inside the runbook, beside the step it describes, as a fenced
code block. The document still renders correctly in GitHub, Confluence, Notion
and Backstage with no plugin.

````markdown
---
sonde:
  version: 1
  id: payments-scale-up
  environment: prod-eu-1
  criticality: high
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

That second check is the one no other tool asks: not "is the infrastructure
healthy", but "does the person following this document still have the permission
the document assumes".

## Status

Early, but working end to end.

| | |
|---|---|
| `sonde parse` | Reads runbooks, executes nothing. Safe on untrusted input. |
| `sonde check` | Runs all nine v1 checks read-only. `--format human\|json\|junit\|tap`. |
| `sonde probe` | Daemon: one-time-token enrolment, mTLS, signed hash-chained results. |
| `sonde impact` | What a manifest diff or an OpenTofu plan removes, as canonical URIs. |
| `sonde suggest` | Drafts assertions from prose for a human to review. |
| `sonde verify` | Checks a signed evidence bundle offline, with no account. |

The hosted control plane — scheduling, history, the dashboard, the reverse-index
pull request bot, wiki connectors — is a separate, proprietary repository. What
is here runs standalone and always will: no account, no network egress beyond
the infrastructure you are checking.

The format is stable at version 1 — see
[docs/runbook-contract.md](docs/runbook-contract.md).

Running the whole system — control plane, probe, dashboard — is walked through
in `docs/getting-started.md` in the `sonde-cloud` repository.

## Try it

```bash
go run ./cmd/sonde check ./runbooks
```

```
runbooks/payments-scale.md  payments-scale-up
  PASS   payments-deploy-exists   deployment/payments-api in payments exists
  PASS   payments-min-replicas    spec.replicas is 3
  PASS   db-creds-key             secret/db-creds in payments has key PGPASSWORD
  PASS   scale-command            parses; deploy/payments-api exists
  FAIL   oncall-can-scale         system:sre-oncall cannot patch deployments in payments

5 checks: 4 passed, 1 failed

1 runbook wrong: payments-scale-up
```

Everything in that document is accurate except the one thing that matters: the
person the runbook was written for cannot perform its central step.

`sonde parse` does the same reading with nothing executed — safe on untrusted
input — and `--format json` emits the machine contract. Markdown files without a
`sonde:` key in their frontmatter are skipped silently, so pointing either at a
whole documentation tree is fine.

A page from a wiki, which has nowhere to put a YAML header, declares itself with
a `sonde-runbook` block instead — see
[docs/runbook-contract.md](docs/runbook-contract.md).

Exit codes are the contract CI branches on: **0** everything passed, **1** an
assertion failed and a runbook is wrong, **3** a runbook would not parse, **4**
something could not be checked at all. Exit 4 is deliberately not 1 — a cluster
Sonde cannot reach is not a document that is wrong.

## In CI

```yaml
- uses: HamZeus95/sonde@v1
  with:
    paths: ./runbooks
    format: junit
    output: sonde.xml
```

JUnit is the format worth using in CI: it is the only one that distinguishes a
failed assertion from a check that could not be evaluated, and every CI system
already renders that distinction.

## What Sonde is not

- **Not incident-response automation.** Sonde never executes a runbook's steps.
  It runs when nothing is on fire.
- **Not an AI SRE.** It assumes your runbooks are worth keeping accurate rather
  than replacing them with an agent. No LLM sits in the verification path.
- **Not a documentation linter.** Vale checks prose. Sonde checks truth.
- **Not monitoring.** A failing check means the *document* is wrong, not that
  production is down.
- **Not a scorecard.** Other tools check that a runbook exists. A runbook full
  of dead references scores 100% on those.

## Writing the assertions

The hard part of adopting Sonde is not running it — it is writing the first
assertions. `sonde suggest` drafts them from a runbook's prose and prints a
diff:

```bash
sonde suggest ./runbooks/db-failover.md
```

This is the only command that uses a language model, and the only place in
Sonde where one is permitted. It drafts; it never verifies. Every block it
proposes is run through the real parser before you see it, anything that would
not parse is reported as rejected with the reason, and what lands in your
repository is what you read and chose to keep. Needs `ANTHROPIC_API_KEY`, and
sends the document's prose to the Claude API — point it at documents you are
willing to send.

## Before you merge

`sonde impact` reads a change and prints the resources it deletes, as the same
canonical URIs the parser produces for your runbooks:

```bash
sonde impact --before base/ --after head/          # kubernetes manifests
sonde impact --plan <(tofu show -json tfplan)      # opentofu or terraform
```

Joining those two sets is the reverse index — infrastructure resource to the
runbooks that reference it — and it is what lets a pull request be told, before
it merges, that it breaks three documented procedures. The hosted control plane
does that join and comments on the pull request; the command itself is here, in
the open source CLI, because the canonicalisation has to be identical on both
sides.

## Running as a probe

For continuous verification rather than a CI run, `sonde probe` runs as a daemon
inside your network:

```bash
SONDE_ENROLMENT_TOKEN=... sonde probe \
  --control-plane https://sonde.example.com \
  --environment prod-eu-1
```

In a cluster, install the chart:

```bash
helm install sonde-probe ./charts/sonde-probe \
  --namespace sonde --create-namespace \
  --set controlPlane.url=https://sonde.example.com \
  --set probe.environment=prod-eu-1 \
  --set enrolment.existingSecret=sonde-enrolment
```

The grant is `get` and `list`, and nothing else — no `watch`, no verb that
changes anything. `can_i` checks about other identities need one more grant, off
by default, and report `error` rather than `fail` without it. See
[charts/sonde-probe](charts/sonde-probe/README.md).

It makes only outbound connections. It listens on no port, generates its signing
key inside the pod, and never sends a credential anywhere. Results are signed
with Ed25519 and chained to the result before them, so neither the control plane
nor anyone who reaches its database can rewrite a `fail` into a `pass` or drop a
failing check without it being detectable — see
[docs/evidence.md](docs/evidence.md).

## Evidence an auditor can check

Results carry an Ed25519 signature and are chained to the result before them, so
neither the control plane nor anyone who reaches its database can rewrite a
`fail` into a `pass` or drop a failing check without it being detectable.

A period of that history exports as a bundle, and **verifying one is free**:

```bash
sonde verify ./sonde-evidence-2026-Q3.tar.gz
```

```
sonde-bundle-v1
  period      2026-07-01 to 2026-10-01
  runbooks    14, 62 checks
  results     5940 passed, 118 failed, 22 could not be checked, 0 skipped
  probe       eu-1 (prod-eu-1)

6080 results verified. Every signature holds and no result is missing from any chain.
```

No network, no account, nothing to trust but the bundle — and it is read as a
stream, so a bundle covering three years verifies on the same laptop as one
covering a week. Exporting one is a control plane feature; checking one is here,
because evidence only the vendor can verify is not evidence. [docs/evidence.md](docs/evidence.md) has the format
and, just as importantly, what a bundle does *not* prove.

## Read-only, always

Every check is a read. No `create`, `update`, `patch`, `delete`, `exec` or
`scale` is ever issued against your infrastructure, and secret *values* are
never read, logged or transmitted — `secret_key_present` asserts only that a key
is there. [docs/security.md](docs/security.md) has the full threat model and the
exact RBAC grant.

## Licence

Apache-2.0. Contributions are accepted under the
[DCO](https://developercertificate.org/): sign off your commits with
`git commit -s`.
