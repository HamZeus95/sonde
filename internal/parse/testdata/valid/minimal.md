---
sonde:
  version: 1
  id: minimal
---

# Minimal runbook

```sonde
id: only-check
kind: http
check: status_is
url: https://status.corp.example/health?token=redacted#top
```
