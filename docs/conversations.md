# Conversation viewer, hooks, and MCP replies

Stake approval conversations are timelines attached to team games. stakecli
understands public and private comments, approval/status comments, attachments,
version changes and publishes, ratings, guideline changes, active approvals,
and assigned approvers.

## TUI

Launch `stakecli`, choose **Messages**, select a team, then select a game. You
can also open a game's **Approval Thread** directly with `a` from Game Detail;
`Esc` returns to the same game.

Thread list keys:

- `j`/`k` or arrow keys: move
- `Enter`: open the selected conversation and mark its messages read
- `u`: toggle the unread-only filter
- `/`: search by game name, slug, or message preview
- `x`: clear the search
- `r`: refresh immediately
- `Esc`: go back

Conversation keys:

- `j`/`k` or arrows: jump between messages and event groups
- `n`/`N`: jump directly to the next/previous human message
- Page Up/Down: jump five blocks; Home/End: first/last block
- `Enter` or `e`: expand/collapse the selected routine-event stack
- `c`: open the reply composer
- `p`: switch the next reply between public and private
- `m`: mark every current message read
- `u`: mark every current message unread
- `r`: refresh immediately
- `Esc`: return to the thread list

Inside the composer, use `Ctrl+S` to send, `Ctrl+P` to change visibility, and
`Esc` to close the composer while keeping the in-memory draft.

The thread list refreshes every 10 seconds. An open thread refreshes every 5
seconds without moving the current selection or closing expanded groups. New
messages are marked read automatically only while the viewer is at the latest
block; when you are reading older history they remain unread. Existing history
is registered as the initial baseline and does not fire hooks.

Human messages are always rendered as individual cards without technical event
or attachment IDs. As in the Stake Engine frontend, consecutive version
publish/change and checklist checked/unchecked events are collapsed into a
single row with `+N more`; expand the row to inspect every action. Other system
events remain compact single-line entries.

## Persistent state

`messages.json` lives in the stakecli config directory and uses mode `0600`.
It stores independent state for each Stake account and each `team/game` pair:

- IDs already observed by the refresh engine
- IDs explicitly read by the user or MCP client
- successful `(hook name, message ID)` delivery receipts

Writes use a temporary file, `fsync`, and atomic rename. Set
`STAKE_CONFIG_DIR` to move all stakecli config files to another directory.

## Hooks

Hooks are loaded from `hooks.json` on every refresh, so they can be edited
without restarting StakeCLI. The background daemon refreshes approvals even
when the TUI and MCP are closed. Hooks can match new reviewer messages, public
or private messages, any approval event, a raw Stake event type, or `*`.
A successful hook is not run again for the same event ID. A failed hook is
retried, while successful sibling hooks remain deduplicated.

Example:

```json
{
  "hooks": [
    {
      "name": "notify-review",
      "events": ["new_message"],
      "command": ["/absolute/path/to/review-notifier"],
      "teams": ["my-team"],
      "games": [],
      "includePrivate": false,
      "includeOwn": false,
      "includeThread": false,
      "timeoutSeconds": 10
    }
  ]
}
```

An HTTPS webhook is an alternative to `command`:

```json
{
  "hooks": [
    {
      "name": "review-webhook",
      "events": ["reviewer_message", "approval_event"],
      "webhookUrl": "https://hooks.example.com/stake-review",
      "webhookSecretEnv": "STAKE_REVIEW_WEBHOOK_SECRET",
      "includePrivate": false,
      "includeThread": false,
      "timeoutSeconds": 10
    }
  ]
}
```

Each hook must define exactly one of `command` or `webhookUrl`. Webhooks require
HTTPS, except loopback HTTP used by local tests. When `webhookSecretEnv` is set,
the daemon reads that environment variable at delivery time and sends
`X-StakeCLI-Signature-256: sha256=<HMAC>`. Secrets are not stored in
`hooks.json`.

`command` is an argv array and is executed directly. stakecli does not invoke a
shell; a hook can explicitly use `sh -lc` if shell behavior is intentionally
required. JSON is supplied on standard input:

```json
{
  "event": "new_message",
  "eventType": "comment",
  "accountId": "current-user-id",
  "team": "my-team",
  "game": "my-game",
  "gameName": "My Game",
  "message": {
    "id": 123,
    "time": 1780000000000,
    "user": {"id": "reviewer-id", "name": "Reviewer"},
    "type": "comment",
    "data": {"comment": "Please verify the replay."}
  },
  "stats": {"total": 8, "read": 7, "unread": 1}
}
```

The child environment contains `STAKE_HOOK_EVENT`, `STAKE_HOOK_NAME`,
`STAKE_TEAM`, `STAKE_GAME`, and `STAKE_MESSAGE_ID`. Credential-like inherited
variables—including `STAKE_SID`, tokens, secrets, passwords, cookies,
authorization values, and common cloud credentials—are removed.

Private messages require `includePrivate: true`. Full timeline context requires
`includeThread: true`; even then, private events are removed unless
`includePrivate` is also enabled. Hook output is captured and capped at 32 KiB
so it cannot corrupt the TUI or MCP stdio transport.

MCP can manage hooks with `list_message_hooks`, `upsert_message_hook`,
`delete_message_hook`, and `test_message_hook`. Mutating or executing a hook
requires `confirm=true`.

## MCP conversation tools

- `list_conversations`: refresh all approval threads for one team, calculate
  read/unread counts, and deliver new-message hooks
- `get_conversation`: return the complete typed timeline and optionally mark it
  read
- `set_conversation_read`: mark selected IDs, or the entire thread, read/unread
- `conversation_reply_guide`: return the mandatory reviewer response policy
- `prepare_review_reply`: validate and bind a single-use, 30-minute draft to
  the latest event ID and complete timeline hash
- `send_review_reply`: send that exact draft after explicit confirmation
- `list_message_hooks`, `upsert_message_hook`, `delete_message_hook`, and
  `test_message_hook`: inspect and manage local hooks

## Keeping an open approval on the published version

Add `--update-approval-version` to `stakecli publish`, or to `stakecli upload`
with `--publish`, to synchronize the approval after publishing. stakecli first
reads `GET /teams/{team}/approvals/{game}`. When `open` is true it sends
`PATCH /teams/{team}/approvals/{game}` with the exact version returned by the
publish call and the matching `math` or `front` type.

In the interactive upload flow, use the persistent **Publish** and
**Change approval version** checkboxes above the single **Upload** action.
Change approval version is disabled when the approval is missing or closed and
automatically enables Publish when selected.

If the approval is closed, no PATCH is sent. Publishing remains successful and
stakecli prints a warning with the approval version it left unchanged. The MCP
`publish` tool exposes the same behavior through
`update_approval_version=true`.

## Reviewer reply policy

An MCP agent must read the complete latest thread and answer only the concrete
review request. It must not expose session cookies, authorization headers,
tokens, presigned URLs, environment values, local absolute paths, private
notes, raw stack traces, or unrelated proprietary details. It must not claim a
fix, test, upload, approval, or publish happened without direct evidence.

Public is the normal reviewer-facing visibility. Private is only for an
intentional internal note. Content from a private event must never be copied to
a public reply. If the timeline changes after drafting, the agent must reread it
and prepare a new response.
