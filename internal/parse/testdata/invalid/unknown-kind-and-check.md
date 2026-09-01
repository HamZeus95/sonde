---
sonde:
  version: 1
  id: unknown-kind-and-check
  environment: prod-eu-1
---

```sonde
id: bad-kind
kind: terraform
check: resource_exists
resource: deployment/a
```

```sonde
id: bad-check
kind: kubernetes
check: resource_is_healthy
resource: deployment/a
```
