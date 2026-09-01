---
sonde:
  version: 1
  id: db-failover
  owner: team-platform
  environment: prod-eu-1
  criticality: high
---

# Runbook: Fail the primary database over to the standby

## Step 1 — Confirm you are in the right group

```sonde
id: dba-group-exists
kind: identity
check: group_exists
provider: keycloak
group: db-operators
```

## Step 2 — Confirm the DNS record you are about to move

```sonde
id: db-primary-record
kind: dns
check: record_exists
name: db-primary.internal
type: CNAME
expect: db-eu-1a.internal.
```

## Step 3 — Confirm the standby is reachable

```sonde
id: db-standby-record
kind: dns
check: record_exists
name: db-standby.internal
type: CNAME
```

## Step 4 — Confirm the credentials the failover job reads

The value is never read. This only asserts that the key is still there under the
name the job expects.

```sonde
id: db-creds-key
kind: kubernetes
check: secret_key_present
resource: secret/db-creds
namespace: data
key: PGPASSWORD
```

## Step 5 — Confirm the documented command still refers to real objects

```sonde
id: failover-command
kind: command
check: parses
command: kubectl -n data exec statefulset/postgres-primary -- pg_ctl promote
namespace: data
```

## Step 6 — The status page you will be updating

```sonde
id: status-page
kind: http
check: status_is
url: https://status.corp.example/
```
