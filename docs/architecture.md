# Architecture

stakecli uses ports and adapters so each user interface shares the same
application behavior.

```text
CLI ─┐
TUI ─┼─> service.Service ─> service.Gateway ─> api.Client ─> Stake Engine
MCP ─┘          │
                ├─> upload engine ─> local filesystem + service.Gateway
                │                    └─> optional accelerator ─> presigned object PUT
                ├─> operation registry <─> independent daemon ─> upload engine
                └─> conversation manager <─ daemon polling ─> command/webhook hooks
```

## Packages

- `internal/stake` owns transport-independent domain models.
- `internal/service` owns application use cases and outbound interfaces.
- `internal/api` is the Stake Engine HTTP adapter.
- `internal/upload` contains local scanning, versioned compliance policy and
  metrics, planning, hashing, and upload execution mechanics behind a narrow
  client interface. Compliance returns stable codes and numeric bounds so
  presentation adapters never need to parse human-readable messages.
- `internal/repack` performs bounded, read-only Zstd book sampling and reports
  whether a level-10 repack would save enough bytes to be worth offering. Its
  opt-in CI path creates a private temporary bundle, recompresses only
  worthwhile books, verifies decompressed SHA-256, and never mutates sources.
- `internal/operations` stores credential-free, cross-process upload snapshots
  and desired control states through locked atomic private files.
- `internal/daemon` owns detached process lifecycle, upload queue execution,
  operation controls, heartbeats, and continuous approval polling.
- `internal/conversations` owns approval refresh, exact ID-based read-state,
  hook matching/delivery receipts, sanitized command execution, signed HTTPS
  webhook delivery, and summaries shared by the TUI, daemon, and MCP adapters.
- `internal/tui/upload` persists the interactive Publish and Change approval
  version preferences in the private stakecli config directory using atomic
  file replacement.
- `internal/cli`, `internal/tui`, and `internal/mcpserver` are presentation
  adapters. They format input/output but do not implement Stake workflows.

The executable in `cmd/stake` is the composition root. It resolves a session
credential, creates `api.Client`, wraps it in `service.Service`, and selects the
requested presentation adapter.

The optional accelerator is injected only as the upload engine's
`UploadToS3` transport. The primary gateway still performs scratch listing,
ETag-aware planning, copy/delete, multipart init/complete, and publish. This
keeps the SID and the authoritative upload state machine inside StakeCLI.
Accelerator tasks use password-authenticated certificate pinning and
independently authenticated AES-GCM frames. Interrupted staging resumes at the
daemon-confirmed ciphertext offset for one hour; committed encrypted tasks are
then delivered and recovered across daemon restarts without a client
connection.

## MCP boundary

`stakecli mcp` is a local stdio server. Standard output is reserved exclusively
for MCP JSON-RPC messages; diagnostics go to standard error. The server uses
typed input and output schemas and marks each tool with read-only, destructive,
idempotent, and open-world hints.

Credentials belong to process configuration, not the protocol. MCP tools can
report whether authentication is configured and valid, but cannot receive or
return the SID.

Reviewer replies are also two-step. `prepare_review_reply` binds a sanitized
draft to the latest approval event ID and complete timeline hash; `send_review_reply` refuses a
stale or unconfirmed draft. Hook child processes receive JSON on standard input
and a filtered environment that removes credential-like variables.

Upload execution requires a fresh plan fingerprint. The fingerprint covers the
target, action, destination, server-side copy source, ETags, and sizes. Any
local or remote change invalidates the fingerprint before mutations begin.
Math upload fingerprints also cover optional compliance context such as the
display base-mode hint. Tier checks mirror the moptimizer report
validator: platform bet level and currency are excluded from the verdict, and
the maximum compliant bet level is derived from every mode.

`start_upload` validates that fingerprint, stores a credential-free queued
operation, and ensures the detached daemon is running. The daemon recalculates
the fingerprint before mutation. A different MCP session can pause, resume,
cancel, retry, or inspect live byte, file, multipart, completion, and error
state. State directories use mode `0700`, records use mode `0600`, and
atomic-write temporary files are removed immediately.

## Compliance boundary

The application service exposes the current policy independently from bundle
evaluation. MCP maps this to `get_compliance_policy` and
`check_math_compliance`; CLI and TUI use the same service-compatible result
model. See [Math compliance API](compliance.md) for formulas, tier behavior,
input context, and the versioned response contract.
