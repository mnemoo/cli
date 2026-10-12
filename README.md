# stakecli

An open-source CLI and TUI for managing game assets on the
[Stake Engine](https://studio.engine.io) RGS. Upload math and front-end
bundles, run compliance checks, and publish releases from your terminal
or CI pipeline.

> **Disclaimer:** this tool is not affiliated with the Stake Engine team.
> It is built by the community, privacy- and safety-first. We are in touch
> with the Stake Engine team about shipping a more convenient way to
> obtain API tokens — until then, session cookies are used (see
> [Authentication](#authentication) below).

## Features

- Interactive TUI with team/game browser, upload wizard and publish flow
- Auto-refreshing approval conversation viewer with persistent read/unread state
- Hot-reloaded message hooks with per-hook, per-message delivery receipts
- Non-interactive CLI mode for CI/CD (`stakecli upload ...`)
- Optional encrypted Linux upload accelerator with automatic certificate pinning
- Native local MCP server for coding agents (`stakecli mcp`)
- Versioned advisory 2-star/3-star math compliance checks for interactive and headless workflows
- Built-in MLR reweighting for headerless LUT CSV files (`stakecli mlr`)
- Self-update (`stakecli update`)
- Zero external runtime dependencies (single static binary)

## Install

### Linux (Debian/Ubuntu)

```bash
curl -LO https://github.com/mnemoo/cli/releases/download/v2.0.0-beta.1/stakecli_2.0.0-beta.1_linux_amd64.deb
sudo dpkg -i stakecli_2.0.0-beta.1_linux_amd64.deb
```

### Linux (RHEL/Fedora)

```bash
sudo rpm -i https://github.com/mnemoo/cli/releases/download/v2.0.0-beta.1/stakecli_2.0.0-beta.1_linux_amd64.rpm
```

### macOS (Apple Silicon)

```bash
curl -L https://github.com/mnemoo/cli/releases/download/v2.0.0-beta.1/stakecli_2.0.0-beta.1_darwin_arm64.tar.gz | tar -xz
sudo mv stakecli /usr/local/bin/
# If Gatekeeper blocks the binary, clear the quarantine flag:
sudo xattr -d com.apple.quarantine /usr/local/bin/stakecli
```

### Windows

One-line install in PowerShell (downloads the latest release, installs
to `%LOCALAPPDATA%\stakecli` and adds it to your user `PATH`):

```powershell
irm https://raw.githubusercontent.com/mnemoo/cli/main/install.ps1 | iex
```

Or, to install a specific version:

```powershell
$env:STAKECLI_VERSION = "v2.0.0-beta.1"
irm https://raw.githubusercontent.com/mnemoo/cli/main/install.ps1 | iex
```

Prefer a manual install? Download the matching `.zip` archive from the
[latest release](https://github.com/mnemoo/cli/releases/latest) and
extract `stakecli.exe` somewhere on your `PATH`.

### From source

```bash
git clone https://github.com/mnemoo/cli.git
cd cli
go build -o stakecli ./cmd/stake
```

Go 1.25+ is required.

### Linux upload accelerator

The optional accelerator keeps all upload planning, ETag comparison,
multipart completion, publish behavior, and the Stake SID inside `stakecli`.
Only presigned PUT streams selected by the normal upload plan are relayed.

Install the `amd64` or `arm64` systemd daemon from a GitHub release:

```bash
curl -fsSL \
  https://github.com/mnemoo/cli/releases/latest/download/install-accelerator.sh |
  sudo bash
```

The installer asks for a password through `/dev/tty`, generates a self-signed
TLS identity, installs the hardened service, enables it at boot, and opens the
daemon on TCP port `7775`. Firewall changes remain explicit.

Connect by IP. The certificate is pinned automatically only after the password
has authenticated the first connection:

```bash
stakecli accelerator add --name relay 203.0.113.10:7775
stakecli accelerator speedtest --accelerator relay
stakecli upload ... --accelerator relay
stakecli accelerator tasks --accelerator relay --watch
```

Interactive uploads include a complete connection wizard: choose direct or a
saved relay, or add a relay by IP, optional name, and masked password. A new
identity is authenticated and pinned automatically, after which the TUI can run
an encrypted 64 MiB speed test before selecting the relay for that upload.
Interrupted client-to-relay streams resume from the last accepted encrypted
offset for up to one hour. After a complete part reaches the relay, it continues
delivery without a client connection; rerunning the same upload reconciles
completed remote parts and matching ETags.

See [Encrypted upload accelerator](./docs/accelerator.md) for the protocol,
security boundary, streaming behavior, CI/CD usage, and recovery semantics.

### Optional Zstd book repack

The TUI does not analyze books automatically. When compressed math books total
at least 100 MiB, it offers a read-only Level 10 estimate first. The normal CLI
only runs the estimator when explicitly requested:

```bash
stakecli repack estimate --level 10 ./publish_files
```

CI/headless jobs can opt into an automatic verified repack:

```bash
stakecli upload \
  --team myteam --game mygame --type math --path ./publish_files \
  --yes --repack
```

`--repack` only uses files that save at least 5% and 16 MiB both in the estimate
and in the actual result. It works in private temporary staging, verifies the
uncompressed SHA-256, never modifies originals, and cleans staging afterward.
The TUI exposes the same operation as a reversible confirmation-screen button.
It uses the platform system temp directory, shows temporary-space, automatic
CPU-worker count, and a matching conservative working-memory requirement first,
and never writes decompressed JSONL to disk. Estimation and full repack use up
to 8 available CPU workers for each book; the TUI shows live compression and
SHA-256 verification progress with bytes, percentage, and elapsed time.

Repack is **not recommended by default**: Level 10 and the verification pass
can consume substantial CPU, memory, time, and temporary disk space. It mainly
helps very large, weakly compressed books on slow upload routes. See
[Zstd book repack estimator](./docs/repack.md).

## Quick start

```bash
# Launch the interactive TUI
stakecli

# Check a local math bundle without authentication or network access
stakecli compliance .

# Preview an automatic MLR adjustment; add --save to overwrite the LUT
stakecli mlr ./publish_files/indexes/base_0.csv --target-n 250

# Estimate whether existing Zstd books can be packed better (read-only)
stakecli repack estimate --level 10 ./publish_files

# Or upload from the command line
stakecli upload \
  --team myteam \
  --game mygame \
  --type math \
  --path ./dist \
  --compliance \
  --yes --publish

# Or select account/team/game interactively, then upload and publish
stakecli publish math .

# Headless front-end publish
stakecli publish front ./dist --headless --team myteam --game mygame

# Also move an open approval to the version returned by publish. If the
# approval is closed, publish still succeeds and only prints a warning.
stakecli publish front ./dist --headless --team myteam --game mygame \
  --update-approval-version

# Headless math skips compliance by default; add --compliance to print the
# report before continuing. Both 2-star and 3-star are always analyzed.
stakecli publish math ./dist --headless --team myteam --game mygame
stakecli publish math ./dist --headless --team myteam --game mygame \
  --compliance
```

### CLI flags

```
stakecli              Launch interactive TUI
stakecli compliance   Check a local math bundle
stakecli mlr          Reweight a LUT to adjust its most-likely result
stakecli repack       Estimate better Zstd compression for math books
stakecli upload       Upload files to Stake Engine
stakecli publish      Upload and publish math or front-end assets
stakecli accelerator  Configure and inspect encrypted upload relays
stakecli mcp          Run the native MCP server over stdio
stakecli update       Check for and install new releases
stakecli version      Show version and build info
stakecli help         Show this help

Math upload/publish compliance flags:
  --compliance           Show advisory compliance in headless mode
  --base-mode NAME       Optional display base-mode hint
  --json                 Print structured JSON

Upload flags:
  --team      Team slug (required)
  --game      Game slug (required)
  --type      Upload type: math or front (required)
  --path      Path to local directory (required)
  --yes       Skip confirmation prompts (for CI/CD)
  --publish   Publish after upload
  --update-approval-version Update an open approval to the published version
  --compliance Run advisory math compliance before upload
  --base-mode Optional display base-mode hint (math only)
  --accelerator Profile name or direct IP:port
  --accelerator-password-file Read the relay password from a protected file
  --accelerator-fallback Relay failure behavior: fail or direct
  --direct Upload directly without the interactive relay choice
  --estimate-repack Estimate Zstd level 10 savings without changing files
  --repack Repack only worthwhile Zstd books in temporary staging

MCP flags:
  --sid       Stake session credential (prefer STAKE_SID or keyring)
  --key       Alias for --sid
```

The interactive upload confirmation has one **Upload** action with persistent
**Publish** and **Change approval version** checkboxes. The approval checkbox is
available only while the selected game has an open approval, and enabling it
also enables Publish. Preferences are saved atomically in
`upload-preferences.json` under the stakecli config directory and reused on the
next upload.

### MLR reweighting

`stakecli mlr` adjusts the single heaviest LUT row so the most-likely result is
no more frequent than `1 in N`. It only changes weights: rows are not added,
removed, or reordered, and IDs remain aligned with payout multipliers.

The automatic operation is the default. It selects a donor band with the
smallest RTP disturbance and enables RTP locking:

```bash
stakecli mlr ./base.csv --target-n 250
stakecli mlr ./base.csv --target-n 250 --save
```

Every modifying operation is a dry run unless `--save` is present. Use
`inspect` to compare donor options or `shift` to choose one explicitly:

```bash
stakecli mlr inspect ./base.csv --target-n 250
stakecli mlr shift ./base.csv --target-n 250 --donor 0-1.00 --rtp-lock
stakecli mlr ./base.csv --target-n 250 --json
```

The accepted input is a headerless `id,weight,payoutMultiplier` CSV, where the
payout multiplier is stored in cents (`100` means `1.00x`). See
[MLR reweighting algorithm](./docs/mlr.md) for the algorithm and feasibility
limits.

### Environment variables

| Variable | Description |
| --- | --- |
| `STAKE_SID` | Session ID for authentication. Bypasses the keyring; required for CI/CD. |
| `STAKE_API_URL` | Override API base URL. Default: `https://studio.engine.io/api`. |
| `STAKE_NO_UPDATE_CHECK` | Set to any value to disable the background update check. |
| `STAKE_STATE_DIR` | Override the private local directory used for cross-session upload progress. |
| `STAKE_CONFIG_DIR` | Override the private config directory containing accounts, message state, and hooks. |
| `STAKE_ACCELERATOR` | Default accelerator profile or direct IP address. |
| `STAKE_ACCELERATOR_PASSWORD` | Accelerator password. Use a CI secret or interactive prompt. |
| `STAKE_ACCELERATOR_FALLBACK` | `fail` by default; set `direct` only for an explicit direct-upload fallback. |

## Authentication

`stakecli` authenticates against the Stake Engine API with the same
session cookie (`sid`) your browser uses on
[studio.engine.io](https://studio.engine.io). On interactive launch, you
will be asked to paste your SID; it is then stored securely in your OS
keychain and reused on subsequent runs.

> We are working with the Stake Engine team on adding a more convenient
> way to obtain API tokens. Once that lands, `stakecli` will support the
> new flow as a first-class option and keep SID login as a fallback.

### How to get your `sid` cookie

1. Open your browser and log in to <https://studio.engine.io> as usual.
2. Open DevTools:
   - **Chrome / Edge / Brave:** `F12`, or `Cmd+Option+I` (macOS) / `Ctrl+Shift+I` (Windows/Linux)
   - **Firefox:** `F12`, or `Cmd+Option+I` / `Ctrl+Shift+I`
   - **Safari:** enable the Develop menu in Preferences → Advanced, then `Cmd+Option+I`
3. Navigate to the cookies panel:
   - **Chrome / Edge / Brave:** `Application` tab → left sidebar → `Storage` → `Cookies` → `https://studio.engine.io`
   - **Firefox:** `Storage` tab → `Cookies` → `https://studio.engine.io`
   - **Safari:** `Storage` tab → `Cookies` → `studio.engine.io`
4. Find the row where **Name** is `sid`.
5. Double-click the **Value** cell and copy the full string.
6. Paste it into the `stakecli` login screen, or export it for CI:
   ```bash
   export STAKE_SID="paste-value-here"
   ```

> **Treat the `sid` like a password.** Anyone with this value can act as
> you on Stake Engine until the session expires. Do not
> commit it to git, share it in screenshots, or paste it into untrusted
> terminals. For CI/CD, store it as an encrypted secret
> (e.g. GitHub Actions `secrets.STAKE_SID`).

## MCP server

`stakecli mcp` runs a native Model Context Protocol server over standard
input/output. It is part of the same static Go binary as the CLI and TUI; no
Node.js or Python runtime is required. It works with local MCP clients such as
Codex, Claude Code, Cursor, VS Code, and GitHub Copilot.

The server exposes typed tools for browsing teams and games, time-ranged team
and game analytics, reading and replying to approval conversations, managing
command/webhook hooks, reading the current compliance policy, checking local
math bundles, inspecting and changing LUT MLR, planning uploads, controlling
background upload operations, and publishing releases. Tool results use
structured JSON schemas.

CLI publish flows accept `--update-approval-version`; the MCP `publish` tool
uses `update_approval_version=true`. Both read the current approval after a
successful publish and update its `mathVersion` or `frontVersion` to the exact
version returned by Stake Engine. A closed approval is a warning-only no-op.

Math compliance is a shared application API rather than UI-specific logic. It
returns stable check codes, numeric bounds, hard-limit status, complete 2-star
and 3-star eligibility, and per-mode RTP, volatility, hit-rate, max-win, CVaR,
and ETL metrics. Platform bet level and currency are excluded from the verdict;
the result reports the calculated maximum compliant bet level instead. See
[Math compliance API](docs/compliance.md).

Run the same checker directly without authentication or network access:

```bash
stakecli compliance .
stakecli compliance ./publish_files --base-mode base
stakecli compliance . --json
```

### Authentication

Credential lookup order:

1. `--sid VALUE` or its `--key VALUE` alias
2. `STAKE_SID`
3. the active account in the OS keyring, created by logging in through the TUI

The key is never exposed as an MCP tool argument or result. Logging in once
with the TUI and then configuring agents with only `stakecli mcp` is the safest
and most portable setup.

You can explicitly insert a key when an MCP client cannot forward environment
variables:

```bash
stakecli mcp --key "your-sid-value"
```

Command-line arguments may be visible in process listings and MCP config files.
Prefer the keyring or `STAKE_SID`, and never commit a credential-bearing config.

### Codex

Project or user `config.toml`:

```toml
[mcp_servers.stake]
command = "stakecli"
args = ["mcp"]
env_vars = ["STAKE_SID"]
tool_timeout_sec = 600
```

Export `STAKE_SID` before launching Codex, or omit `env_vars` to use the
stakecli keyring.

### Claude Code, Cursor, and Copilot

Claude Code and clients using the common `mcpServers` format can launch the
same stdio command:

```json
{
  "mcpServers": {
    "stake": {
      "type": "stdio",
      "command": "stakecli",
      "args": ["mcp"]
    }
  }
}
```

Use this as `.mcp.json` where supported, or paste the server entry into the
client's MCP settings. Launch the client with `STAKE_SID` exported when the OS
keyring is not being used.

VS Code uses the same command under a `servers` key in `.vscode/mcp.json`:

```json
{
  "servers": {
    "stake": {
      "type": "stdio",
      "command": "stakecli",
      "args": ["mcp"]
    }
  }
}
```

### Safe upload workflow

MCP uploads are deliberately two-step:

1. Call `plan_upload` to inspect uploads, server-side copies, deletions,
   compliance results, and the generated `plan_id`.
2. Call `execute_upload` with that exact `plan_id`, or call `start_upload` to
   queue it in the independent StakeCLI daemon and receive an `operation_id`
   immediately.

Use `list_upload_processes` to see active uploads from every local stakecli
CLI, TUI, and MCP session. Use `get_upload_process` for per-file bytes, S3
confirmation, multipart state, retries, and full errors. Pass
`include_finished=true` to the list tool to inspect recent terminal results.
Finished records are retained for one hour and then removed automatically.
Use `pause_upload`, `resume_upload`, `cancel_upload`, and `retry_upload` to
control daemon-owned work. Cancellation and retry require explicit
confirmation.

The daemon is a detached local process, not a goroutine owned by Codex, Claude,
or the MCP transport. Closing the MCP client does not stop an accepted upload.
The daemon reacquires authentication from `STAKE_SID` or the OS keyring and
revalidates the queued `plan_id` before mutation. Queue files never contain the
SID or presigned URLs.

```bash
stakecli daemon start
stakecli daemon status
stakecli daemon stop
```

See [Background daemon and operations](docs/daemon.md).

The server recalculates the plan immediately before execution. If local files
or remote scratch state changed, execution stops and asks the agent to plan
again. When compliance context is included, failed or otherwise incomplete
math is shown in the plan and requires the explicit acknowledgement
`allow_compliance_failures=true`; it is never a permanent upload block.

Publishing is separate and requires `confirm=true`. MCP math publishing skips
compliance by default. With `check_compliance=true`, it requires `path`,
reruns the advisory checker, and verifies that remote scratch exactly matches
the reviewed local bundle. Both tiers use the same LUT validation mechanism as
the moptimizer report generator. Platform bet level and currency are excluded
from the verdict; StakeCLI reports the maximum compliant bet level calculated
from every mode instead. Reported compliance issues do not block a confirmed
publish.

### Approval conversations

Choose **Messages** in the TUI to browse approval conversations by team. The
thread list refreshes every 10 seconds and an open thread every 5 seconds.
Unread counts are based on exact event IDs and persist per Stake account. The
viewer renders public and private comments, attachments, and every known
approval system event, and can send public or private replies.

From Game Detail, press `a` to open that game's Approval Thread directly.
Messages hide technical IDs and remain individually visible, while consecutive
version and checklist events are collapsed into expandable stacks. Timeline
navigation jumps by semantic block instead of scrolling one terminal line at a
time.

The MCP conversation workflow is deliberately context-bound:

1. Call `get_conversation` to read the complete latest timeline.
2. Call `conversation_reply_guide` and `prepare_review_reply`.
3. Inspect warnings, then call `send_review_reply` with the exact `draft_id`
   and `confirm=true`.

Drafts are bound to the latest event ID and a hash of the complete timeline. If
the reviewer posts or edits a message between preparation and sending, the send
is rejected. Drafts are single-use and expire after 30 minutes. Replies
containing credentials, authorization material, presigned URLs, or local
absolute paths are blocked. See
[Conversation viewer, hooks, and MCP replies](docs/conversations.md) for key
bindings, hook configuration, payloads, and the reviewer reply policy.

The daemon polls every team for new approval activity even when the TUI and MCP
are closed. Hooks may execute a direct argv command or deliver JSON to an HTTPS
webhook. Event filters include `new_message`, `reviewer_message`,
`public_message`, `private_message`, `approval_event`, the raw Stake approval
event type, and `*`.

### Analytics and MLR

`get_team_analytics` returns time-ranged operator bets, turnover, profit,
expected profit, unique users, balance, and payment model.
`get_game_analytics` returns per-mode statistics plus graph buckets for profit,
turnover, and bet count. Both default to the last 30 days.

The local `mlr_inspect`, `mlr_preview_change`, and `mlr_change` tools expose the
built-in LUT reweighter. Preview does not modify the file. `mlr_change` requires
`confirm=true`, writes atomically, verifies the saved rows, and retains a
private sibling backup. MLR changes deliberately do not require a preview
fingerprint.

### CI/CD example — publish a Svelte front-end on every push

The example below builds and publishes a front-end bundle from a
**pnpm + Turborepo** monorepo (the same stack used by the
[StakeEngine web SDK](https://github.com/StakeEngine/web-sdk): Svelte 5 +
Vite + SvelteKit apps under `apps/*`, shared libraries under
`packages/*`). On every push to `main`, CI builds the target app with
Turbo, then uses `stakecli` to upload and publish the resulting `dist`
as a front-end release.

Stack assumed by this workflow:

- **Node** ≥ 22.16
- **pnpm** 10.x with workspaces (`pnpm-workspace.yaml` → `apps/*`, `packages/*`)
- **Turborepo** for orchestration (`turbo run build --filter=cluster`)
- **Vite / SvelteKit** with `@sveltejs/adapter-static` — outputs a
  self-contained static bundle to `apps/<app>/build/` (the web-sdk's
  shared `config-svelte` additionally sets `bundleStrategy: 'inline'`
  and `assetsInlineLimit: Infinity`, so the whole app ships as a single
  inlined HTML, which is exactly what Stake Engine hosts as a game
  front-end)

```yaml
# .github/workflows/publish-front.yml
name: Publish front

on:
  push:
    branches: [main]
  workflow_dispatch:

concurrency:
  group: publish-front-${{ github.ref }}
  cancel-in-progress: true

env:
  APP_NAME: cluster              # workspace package name, e.g. apps/cluster
  STAKE_TEAM: myteam
  STAKE_GAME: cluster

jobs:
  publish:
    runs-on: ubuntu-latest
    steps:
      - name: Checkout
        uses: actions/checkout@v6
        with:
          fetch-depth: 0         # full history so Turbo can compute hashes

      - name: Setup pnpm
        uses: pnpm/action-setup@v4
        # version is read from root package.json `packageManager` field

      - name: Setup Node.js
        uses: actions/setup-node@v6
        with:
          node-version: 24
          cache: pnpm

      - name: Install dependencies
        run: pnpm install --frozen-lockfile

      - name: Cache Turbo
        uses: actions/cache@v4
        with:
          path: .turbo
          key: turbo-${{ github.ref_name }}-${{ github.sha }}
          restore-keys: |
            turbo-${{ github.ref_name }}-
            turbo-

      - name: Build front-end bundle
        run: pnpm turbo run build --filter=${{ env.APP_NAME }}
        # Vite/SvelteKit output lands in apps/${APP_NAME}/build/

      - name: Install stakecli
        uses: jaxxstorm/action-install-gh-release@v2
        with:
          repo: mnemoo/cli
          tag: v1.0.0            # pin a release for reproducible builds

      - name: Upload and publish front-end
        env:
          STAKE_SID: ${{ secrets.STAKE_SID }}
        run: |
          stakecli upload \
            --team  "$STAKE_TEAM" \
            --game  "$STAKE_GAME" \
            --type  front \
            --path  "apps/${APP_NAME}/build" \
            --yes --publish
```

What each step does:

1. **Checkout with full history** — Turborepo uses git for its content-
   based cache keys; `fetch-depth: 0` makes local caching effective.
2. **`pnpm/action-setup@v4`** — installs the pnpm version declared in
   the root `package.json`'s `packageManager` field (e.g. `pnpm@10.5.0`).
3. **`actions/setup-node@v6` with `cache: pnpm`** — Node 22 to satisfy
   the repo's `engines.node`, plus lockfile-aware pnpm store caching.
4. **`pnpm install --frozen-lockfile`** — reproducible install; fails
   the build if `pnpm-lock.yaml` is out of sync.
5. **Turbo cache (`actions/cache@v4` on `.turbo`)** — skips re-building
   unchanged packages across runs. Swap for the Turbo remote cache if
   you have one.
6. **`pnpm turbo run build --filter=<app>`** — builds only the target
   app and its local workspace dependencies.
7. **Install stakecli** — via
   [`jaxxstorm/action-install-gh-release`](https://github.com/marketplace/actions/install-a-binary-from-github-releases),
   pinned to a release tag.
8. **`stakecli upload --type front … --publish`** — uploads the
   Vite/SvelteKit `build/` output and publishes the new front version
   in a single call.

> Store `STAKE_SID` as an encrypted repository secret
> (`Settings → Secrets and variables → Actions → New repository secret`).
> Pin `tag:` to an explicit `stakecli` release for reproducibility; omit
> it to always install the latest release.

## Development

```bash
# Run tests
go test ./...

# Lint
golangci-lint run

# Build all binaries into bin/
make build

# Run the local mock API + demo bundle (see DEMO.md)
make demo-build
make mockapi       # terminal 1
make demo-run      # terminal 2
```

See [DEMO.md](./DEMO.md) for the full demo/development setup using the
bundled mock API.

See [docs/architecture.md](./docs/architecture.md) for the application-layer
boundaries shared by CLI, TUI, and MCP.

## Contributing

Issues and PRs are welcome. Please keep changes focused and include tests
where reasonable. Run `go test ./...` and `golangci-lint run` before
opening a PR.

## License

See [LICENSE](./LICENSE).
