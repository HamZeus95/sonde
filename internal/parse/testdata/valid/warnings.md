---
sonde:
  version: 1
  id: warnings
  critcality: high
---

# Everything here parses, and everything here is worth a warning

The runbook declares no environment, so a kubernetes check with no cluster of
its own runs but cannot be indexed.

```sonde
id: unindexable
kind: kubernetes
check: resource_exists
resource: deployment/payments-api
namespace: payments
```

```sonde
id: unknown-fields
kind: dns
check: record_exists
name: db-primary.internal
type: CNAME
namespaces: typo
retries: 3
```

```sonde
id: unindexable-group
kind: identity
check: group_exists
provider: keycloak
group: sre-oncall
```
