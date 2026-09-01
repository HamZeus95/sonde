---
sonde:
  version: 1
  id: payments-scale-up
  owner: team-payments
  environment: prod-eu-1
  criticality: high
---

# Runbook: Scale the Payments API

Use this when the payments API is saturated and the queue depth alert has been
firing for more than ten minutes.

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

If this check fails, the runbook is unrunnable by the person it was written for,
however correct the rest of it is.

```sonde
id: oncall-can-scale
kind: kubernetes
check: can_i
verb: patch
resource: deployments
namespace: payments
as_group: system:sre-oncall
```

## Step 3 — Confirm the floor we are scaling from

```sonde
id: payments-min-replicas
kind: kubernetes
check: field_gte
resource: deployment/payments-api
namespace: payments
path: spec.replicas
value: 3
```

## Step 4 — Watch the dashboard

```sonde
id: payments-dashboard
kind: http
check: status_is
url: https://grafana.corp.example/d/abc123?orgId=1&from=now-6h
expect: 200
```

Scale up:

    kubectl -n payments scale deploy payments-api --replicas=8
