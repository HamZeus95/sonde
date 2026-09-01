---
sonde:
  version: 1
  id: missing-fields
  environment: prod-eu-1
---

```sonde
id: no-resource
kind: kubernetes
check: resource_exists
namespace: payments
```

```sonde
kind: http
check: status_is
url: https://example.com
```

```sonde
id: malformed-resource
kind: kubernetes
check: resource_exists
resource: payments-api
namespace: payments
```

```sonde
id: named-type
kind: kubernetes
check: can_i
verb: patch
resource: deployment/payments-api
```

```sonde
id: sliced-subresource
kind: kubernetes
check: can_i
verb: create
resource: pods
subresource: exec/nested
```

```sonde

```
