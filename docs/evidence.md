# Evidence: signed, hash-chained results

Format version: **sonde-evidence-v1**

A Sonde result is not just a row in a database. It is signed by the probe that
produced it and chained to the result before it, so that a reader — an auditor,
a regulator, or a customer who does not trust the vendor — can establish two
things without trusting the control plane:

1. **Nothing was altered.** A `fail` cannot be rewritten into a `pass`.
2. **Nothing was dropped.** A failing check cannot be quietly removed from the
   history.

That is what makes a drift dashboard usable as evidence for DORA Art. 11,
ISO 27001 A.5.24–5.26 and SOC 2 CC7.3–CC7.4, which ask for procedures that are
documented *and tested*.

---

## 1. What is signed

Each result is reduced to a **preimage**: a fixed set of fields, one per line,
in a fixed order.

```
sonde-evidence-v1
prev:<hex of the previous entry's self_hash, empty for the first>
probe:<probe id>
runbook:<runbook id, the frontmatter sonde.id>
check:<check id, the block's id>
status:<pass|fail|error|skipped>
ran_at:<RFC3339, UTC, whole seconds, Z>
summary:<hex SHA-256 of the observed summary, UTF-8>
```

Every line ends with `\n`, including the last.

- `self_hash` = hex SHA-256 of those bytes.
- `signature` = base64 Ed25519 over the **32 raw bytes** of `self_hash`, using
  the probe's private key.

Signing the hash rather than the preimage means a verifier holding only the
stored hashes can walk an entire chain without reconstructing a single preimage.

### Why a line format and not JSON

The probe writes this in Go and the control plane verifies it in TypeScript.
Two languages agreeing on canonical JSON — key order, number formatting, unicode
escaping, float representation — is a bug waiting to happen, and the bug would
surface as "your evidence does not verify" months later. Lines with a fixed
order and fixed fields cannot drift.

The summary appears as its own digest so that arbitrary text — newlines, emoji,
anything — can never break the format.

### What is deliberately not covered

Observed **detail** is outside the chain. It exists only when a check sets
`verbose: true`, and what the evidence attests is that a named check had a given
status at a given time, attested by a given probe, in an unbroken sequence.
Detail is colour; status is the claim.

## 2. The chain

One chain per probe, in submission order.

```
entry₀.prev_hash = ""                    (genesis)
entryₙ.prev_hash = entryₙ₋₁.self_hash
```

A verifier walks the chain from the genesis, checking at each step that
`prev_hash` matches the previous `self_hash` and that the signature verifies
against the probe's registered public key. Removing an entry breaks the link;
altering one changes its hash and breaks both its own signature and the next
entry's link.

The probe keeps its own tail in `probe.json` and the control plane returns the
tail it holds with every job lease. **If the two disagree, the probe refuses to
submit.** A control plane that has forgotten, truncated or rewritten a probe's
history is detectable rather than silently accommodated.

## 3. Keys

The probe generates an Ed25519 keypair inside the pod at enrolment. The private
key is written to `key.pem` with owner-only permissions and never leaves the
machine — it is not sent to the control plane, is not recoverable from anything
the control plane stores, and is not backed up. A probe that loses its state
directory re-enrols as a new probe with a new chain rather than resuming an old
one.

The control plane stores only the public key, and uses it to verify. It cannot
produce a signature that verifies against it.

## 4. Evidence bundles

An **evidence bundle** is a period of check history, exported for someone
outside the vendor to check. It is a directory, or a `.tar.gz` of one:

```
manifest.json     what this covers, and the public keys needed to verify it
runs.jsonl        one result and chain entry per line, in chain order
runbooks.json     what was being asserted, at export time
controls.json     which control each piece of evidence speaks to
```

Format version: **sonde-bundle-v1**. A verifier that does not recognise the
version refuses rather than guessing — half-understood evidence is worse than
none.

`manifest.json` carries a SHA-256 of every other file, so a bundle edited after
export does not verify. It also lists every probe whose signatures appear, with
the public half of each key, and a `chain_start` per probe — the hash the first
included run follows. That last field is what lets one quarter out of three
years still be a chain rather than an unexplained gap.

### Verifying one

```bash
sonde verify ./sonde-evidence-2026-Q3.tar.gz
```

Exporting a bundle is a control plane feature. **Verifying one is free and open
source**, because evidence that only the vendor can check is not evidence. The
command needs no network, no control plane and no account, and the party being
audited cannot influence its result.

Exit 0 means every signature held and no result is missing from any chain.
Exit 1 means it did not, and each problem is named.

### What a bundle proves, and what it does not

It proves that every result in it was signed by a key one of the customer's
probes held, and that none was altered or removed after signing.

It does not prove that a probe was pointed at the right cluster — that is the
customer's own configuration — and it cannot, on its own, prove that a control
plane never omitted an entire probe. Two things address the second: the manifest
lists every probe it knows of, and each probe keeps its own chain tail locally,
so an operator can compare the tail in `sonde verify --format json` against the
`last_hash` in the probe's state directory. A control plane that dropped a probe
from an export is visible the moment anyone checks.

Saying this plainly is deliberate. An auditor who discovers a limit you did not
disclose stops trusting everything else you said.

## 5. Test vectors

[`evidence-vectors.json`](evidence-vectors.json) holds the preimages and hashes
for a set of results, including empty summaries and summaries with newlines and
non-ASCII text. Both implementations are tested against it:

- Go — `internal/probe/evidence_test.go`
- TypeScript — `packages/contracts` in `sonde-cloud`

Regenerate with `go test ./internal/probe -run TestVectors -update`, and expect
the TypeScript tests to fail until they are re-run against the new file. That
failure is the point: the two implementations cannot drift apart quietly.
