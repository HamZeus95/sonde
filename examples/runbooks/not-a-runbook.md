---
title: Onboarding notes
tags: [internal]
---

# Onboarding

This file has frontmatter but no `sonde:` key, so Sonde skips it silently.

```sonde
id: this-is-never-read
kind: kubernetes
check: resource_exists
resource: deployment/nothing
```

Even the block above is ignored: a document opts in through its frontmatter, not
by containing a fenced block.
