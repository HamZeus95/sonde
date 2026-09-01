---
sonde:
  version: 1
  id: catalogue
  owner: team-platform
  environment: prod-eu-1
  criticality: medium
---

# One check of every kind

```sonde
id: exists
kind: kubernetes
check: resource_exists
resource: Deployments.apps/payments-api
namespace: payments
```

```sonde
id: field-equals
kind: kubernetes
check: field_equals
resource: deploy/payments-api
namespace: payments
path: spec.template.spec.containers[0].image
value: "ghcr.io/corp/payments-api:1.4.2"
```

```sonde
id: field-gte
kind: kubernetes
check: field_gte
resource: deployment/payments-api
namespace: payments
path: spec.replicas
value: 3
timeout: 5s
```

```sonde
id: can-i-self
kind: kubernetes
check: can_i
verb: get
resource: pods
namespace: payments
```

```sonde
id: can-i-group
kind: kubernetes
check: can_i
verb: patch
resource: deployments
namespace: payments
as_group: system:sre-oncall
```

```sonde
id: can-i-subresource
kind: kubernetes
check: can_i
verb: create
resource: pods
subresource: exec
namespace: data
as_group: system:sre-oncall
```

```sonde
id: secret-key
kind: kubernetes
check: secret_key_present
resource: secret/db-creds
namespace: data
key: PGPASSWORD
```

```sonde
id: cluster-scoped
kind: kubernetes
check: resource_exists
resource: namespace/payments
cluster: prod-eu-2
```

```sonde
id: dashboard
kind: http
check: status_is
url: HTTPS://Grafana.Corp.Example:443/d/abc123/
method: HEAD
```

```sonde
id: record
kind: dns
check: record_exists
name: DB-Primary.Internal.
type: cname
expect: db-eu-1a.internal.
```

```sonde
id: group
kind: identity
check: group_exists
provider: keycloak
group: sre-oncall
realm: platform
```

```sonde
id: documented-command
kind: command
check: parses
command: kubectl -n payments get deploy payments-api
namespace: payments
verbose: true
```

```sonde
id: kept-but-not-run
kind: kubernetes
check: resource_exists
resource: deployment/legacy-worker
namespace: payments
enabled: false
```

This block is documentation about Sonde, not an assertion:

```sonde-ignore
id: never-parsed
kind: kubernetes
check: resource_exists
resource: deployment/example
```

And this is an ordinary code block:

```yaml
id: also-never-parsed
kind: kubernetes
```
