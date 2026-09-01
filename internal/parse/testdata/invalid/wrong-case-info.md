---
sonde:
  version: 1
  id: wrong-case-info
  environment: prod-eu-1
---

```Sonde
id: nearly
kind: kubernetes
check: resource_exists
resource: deployment/a
namespace: payments
```
