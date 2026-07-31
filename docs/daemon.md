# Background daemon and operations

`stakecli daemon` is a detached local process that owns long-running work.
Uploads and reviewer polling therefore continue when Codex, Claude, a terminal,
or the MCP stdio process exits.

## Lifecycle

```bash
stakecli daemon start
stakecli daemon status
stakecli daemon status --json
stakecli daemon stop
```

`start_upload` automatically starts the daemon when needed. Only one daemon may
hold the runtime lock. Its heartbeat, log, and status use private files in the
StakeCLI state directory.

The daemon reads authentication from `STAKE_SID` or the active OS-keyring
account. Upload queue records contain the target, local path, plan ID,
compliance context, non-secret account ID, progress, and desired control state.
They never contain the SID, authorization headers, tokens, or presigned URLs.
Credentials supplied to `stakecli mcp --sid/--key` are inherited only by the
detached child process. A daemon claims jobs only for its authenticated account;
StakeCLI refuses to reuse a live daemon from another account.

## Upload execution

`start_upload` still requires the exact `plan_id` returned by `plan_upload`.
Before executing a queued operation, the daemon rescans local files and remote
scratch. Any change invalidates the plan and fails safely before a mutation.

Available controls:

- `pause_upload`: stop at the next safe transfer boundary
- `resume_upload`: continue and ensure the daemon is running
- `cancel_upload`: cancel queued or active work with `confirm=true`
- `retry_upload`: create a new attempt for failed/cancelled work with
  `confirm=true`; the original record remains available

Control and progress updates use per-operation file locks plus atomic private
writes. Finished records are retained for one hour.

## Reviewer polling

The daemon polls all accessible teams every 15 seconds by default. Configure a
different interval with `STAKE_DAEMON_REVIEW_INTERVAL=30s` or run the foreground
worker with `stakecli daemon run --review-interval 30s`.

The first poll establishes a baseline and does not replay historical events.
Later message and approval event IDs are delivered exactly once to each
successful hook. Failed deliveries are retried without replaying successful
sibling hooks.
