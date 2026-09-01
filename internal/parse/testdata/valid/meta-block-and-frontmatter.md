---
sonde:
  version: 1
  id: from-frontmatter
---

# Both forms

The frontmatter wins, and the block is reported rather than ignored.

```sonde-runbook
version: 1
id: from-the-block
```

```sonde
id: a-check
kind: http
check: status_is
url: https://status.corp.example/
```
