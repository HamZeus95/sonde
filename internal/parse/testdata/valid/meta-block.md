# Runbook: Fail the database over

This page came from Confluence, which has nowhere to put a YAML header, so it
declares itself in a block instead.

```sonde-runbook
version: 1
id: db-failover-wiki
owner: team-platform
environment: prod-eu-1
criticality: high
```

## Step 1 — Confirm the record you are about to move

```sonde
id: db-primary-record
kind: dns
check: record_exists
name: db-primary.internal
type: CNAME
```
