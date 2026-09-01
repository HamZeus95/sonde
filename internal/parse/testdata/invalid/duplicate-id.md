---
sonde:
  version: 1
  id: duplicate-id
  environment: prod-eu-1
---

```sonde
id: same
kind: kubernetes
check: resource_exists
resource: deployment/a
namespace: payments
```

```sonde
id: same
kind: kubernetes
check: resource_exists
resource: deployment/b
namespace: payments
```
