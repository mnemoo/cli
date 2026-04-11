# stakecli

An open-source CLI and TUI for managing game assets on the
[Stake Engine](https://stake-engine.com) RGS. Upload math and front-end
bundles, run compliance checks, and publish releases from your terminal
or CI pipeline.

> **Disclaimer:** this tool is not affiliated with the Stake Engine team.
> It is built by the community, privacy- and safety-first. We are in touch
> with the Stake Engine team about shipping a more convenient way to
> obtain API tokens — until then, session cookies are used (see
> [Authentication](#authentication) below).

## Features

- Interactive TUI with team/game browser, upload wizard and publish flow
- Non-interactive CLI mode for CI/CD (`stakecli upload ...`)
- Math compliance checks before upload
- Self-update (`stakecli update`)
- Zero external runtime dependencies (single static binary)

## Install

### Linux (Debian/Ubuntu)

```bash
curl -LO https://github.com/mnemoo/cli/releases/download/v1.0.0/stakecli_1.0.0_linux_amd64.deb
sudo dpkg -i stakecli_1.0.0_linux_amd64.deb
```

### Linux (RHEL/Fedora)

```bash
sudo rpm -i https://github.com/mnemoo/cli/releases/download/v1.0.0/stakecli_1.0.0_linux_amd64.rpm
```

### macOS (Apple Silicon)

```bash
curl -L https://github.com/mnemoo/cli/releases/download/v1.0.0/stakecli_1.0.0_darwin_arm64.tar.gz | tar -xz
sudo mv stakecli /usr/local/bin/
# If Gatekeeper blocks the binary, clear the quarantine flag:
sudo xattr -d com.apple.quarantine /usr/local/bin/stakecli
```

### Windows

Download the matching `.zip` archive from the
[latest release](https://github.com/mnemoo/cli/releases/latest) and
extract `stakecli.exe` somewhere on your `PATH`.

### From source

```bash
git clone https://github.com/mnemoo/cli.git
cd cli
go build -o stakecli ./cmd/stake
```

Go 1.25+ is required.

## Quick start

```bash
# Launch the interactive TUI
stakecli

# Or upload from the command line
stakecli upload \
  --team myteam \
  --game mygame \
  --type math \
  --path ./dist \
  --yes --publish
```

### CLI flags

```
stakecli              Launch interactive TUI
stakecli upload       Upload files to Stake Engine
stakecli update       Check for and install new releases
stakecli version      Show version and build info
stakecli help         Show this help

Upload flags:
  --team      Team slug (required)
  --game      Game slug (required)
  --type      Upload type: math or front (required)
  --path      Path to local directory (required)
  --yes       Skip confirmation prompts (for CI/CD)
  --publish   Publish after upload
```

### Environment variables

| Variable | Description |
| --- | --- |
| `STAKE_SID` | Session ID for authentication. Bypasses the keyring; required for CI/CD. |
| `STAKE_API_URL` | Override API base URL. Default: `https://stake-engine.com/api`. |
| `STAKE_NO_UPDATE_CHECK` | Set to any value to disable the background update check. |

## Authentication

`stakecli` authenticates against the Stake Engine API with the same
session cookie (`sid`) your browser uses on
[stake-engine.com](https://stake-engine.com). On interactive launch, you
will be asked to paste your SID; it is then stored securely in your OS
keychain and reused on subsequent runs.

> We are working with the Stake Engine team on adding a more convenient
> way to obtain API tokens. Once that lands, `stakecli` will support the
> new flow as a first-class option and keep SID login as a fallback.

### How to get your `sid` cookie

1. Open your browser and log in to <https://stake-engine.com> as usual.
2. Open DevTools:
   - **Chrome / Edge / Brave:** `F12`, or `Cmd+Option+I` (macOS) / `Ctrl+Shift+I` (Windows/Linux)
   - **Firefox:** `F12`, or `Cmd+Option+I` / `Ctrl+Shift+I`
   - **Safari:** enable the Develop menu in Preferences → Advanced, then `Cmd+Option+I`
3. Navigate to the cookies panel:
   - **Chrome / Edge / Brave:** `Application` tab → left sidebar → `Storage` → `Cookies` → `https://stake-engine.com`
   - **Firefox:** `Storage` tab → `Cookies` → `https://stake-engine.com`
   - **Safari:** `Storage` tab → `Cookies` → `stake-engine.com`
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

## Contributing

Issues and PRs are welcome. Please keep changes focused and include tests
where reasonable. Run `go test ./...` and `golangci-lint run` before
opening a PR.

## License

See [LICENSE](./LICENSE).
