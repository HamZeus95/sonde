---
sonde:
  version: 1
  id: bad-values
  environment: prod-eu-1
---

```sonde
id: bad timeout
kind: http
check: status_is
url: https://example.com
timeout: soon
```

```sonde
id: mutating-method
kind: http
check: status_is
url: https://example.com
method: POST
```

```sonde
id: not-a-status
kind: http
check: status_is
url: https://example.com
expect: 9000
```

```sonde
id: unknown-record-type
kind: dns
check: record_exists
name: db.internal
type: PTR
```

```sonde
id: both-subjects
kind: kubernetes
check: can_i
verb: get
resource: pods
as_user: alice
as_group: sre
```
