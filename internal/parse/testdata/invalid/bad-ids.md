---
sonde:
  version: 1
  id: bad-ids
  environment: prod-eu-1
---

```sonde
id: has a space
kind: http
check: status_is
url: https://example.com
```

```sonde
id: -leading-dash
kind: http
check: status_is
url: https://example.com
```
