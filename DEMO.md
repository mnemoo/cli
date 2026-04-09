# stakecli demo setup

Mock API, fixtures, and tooling for recording presentation videos and
developing `stakecli` without access to a production Stake Engine instance.

## What's included

| Path | What it does |
| --- | --- |
| `cmd/mockapi/` | Standalone HTTP server implementing the subset of the Stake Engine API that `stakecli` talks to. Serves hand-crafted fixtures for reads (teams, games, stats, versions, balances) and tracks upload state in memory. |
| `cmd/demo-bundle/` | Deterministic generator for a realistic math + front bundle. Output passes `stakecli`'s math compliance check cleanly (no warnings). |
| `internal/auth/` | Two new env-var hooks (`STAKE_CONFIG_DIR`, `STAKE_KEYRING_DISABLE`) that isolate the demo from the user's real config and OS keychain. |
| `Makefile` | Convenience targets for building, running, and resetting the demo. |
| `testdata/demo-bundle/` | Output of `cmd/demo-bundle`, ~15 MiB across ~15 files. Gitignored. |

## Quick start

```bash
# One-time setup
make demo-build         # build stakecli, mockapi, generator + generate bundle

# Terminal 1 — run the mock API
make mockapi

# Terminal 2 — run stakecli against it, from scratch including login
make demo-run
# enter "demo" (or anything) when the login screen appears
```

## Between recording takes

```bash
make demo-reset         # wipe mock's in-memory scratch + publish state
```

`demo-reset` is the only thing you need between takes. The login screen
shows on every `make demo-run` automatically (see "Auth flow" below), so
there's nothing to clean up on the client side.

## Auth flow

The TUI does full SID-based auth on every launch:

1. `auth.Init()` reads the accounts config file
2. If there's an active account, fetch its SID from the OS keychain
3. Call `GET /users` with `Cookie: sid=<value>` to validate
4. On success, jump straight to the teams screen
5. On failure, show the login screen

The mock already supports all four steps: `GET /users` accepts any non-empty
`sid` cookie and returns a demo user (`Mnemoo (demo)`). So the full pipeline
works out of the box.

**The problem**: if you already have a real stakecli account on your
machine, steps 1–3 succeed against the mock (it accepts any SID), so the
TUI skips the login screen on recording. Worse, the mock's demo user gets
merged into your real accounts config file.

**The fix** — two new env vars, supported by the demo targets:

| Env var | What it does |
| --- | --- |
| `STAKE_CONFIG_DIR=/tmp/stakecli-demo` | Overrides `os.UserConfigDir()/stakecli/`. The demo writes a throwaway `accounts.json` here instead of mangling your real config. |
| `STAKE_KEYRING_DISABLE=1` | Makes `SetSID` / `DeleteSID` no-ops and `GetSID` return `ErrNotFound`. The demo never touches your OS keychain. |

Both env vars are set automatically by every `make demo-*` target. The
combined effect:

- **First run**: config dir empty → `auth.Init()` returns `needLogin=true`
  → login screen shows → enter "demo" → validated against mock → account
  saved to `/tmp/stakecli-demo/accounts.json`, keyring untouched.
- **Next run**: config still has the account, but keyring is disabled so
  `GetSID` fails → login screen shows again. No state carries between runs.
- **Cleanup**: `make demo-logout` nukes `/tmp/stakecli-demo`, or
  `make clean` nukes everything.

These env vars are also legitimately useful for CI, headless containers,
and multi-account workflows — they can be cherry-picked to `main` when you
want.

## Fixtures

The mock ships with 4 teams and 20 games, hand-edited to look impressive
on camera:

- **Neon Labs** — 8 games, ~3.8M monthly plays, the demo's "main" team
  - `gods-of-neon`, `midnight-vault`, `thunder-empress`, `sakura-fortune-x`,
    `nebula-rush` — published, full stats with 2-4 modes each
  - `cyber-samurai`, `plasma-drift`, `ghost-protocol` — unpublished, used as
    upload targets in the demo
- **Aurora Studios** — 5 games
- **Crimson Games** — 4 games
- **Indie Collab** — 3 games

Per-game stats include realistic RTP (~96%), turnover, and profit across
`base`, `freespins`, `bonus_buy`, and `max_buy` modes.

## Demo bundle

`cmd/demo-bundle` generates a seeded, reproducible bundle that passes the
math compliance check with clean output:

- **3 modes** — `base` (cost 1), `freespins` (cost 100), `bonus_buy` (cost 200)
- **RTP**: 96.42% / 96.38% / 96.28% (cross-mode variation 0.14%, under the
  0.5% limit)
- **Max win**: 5000.00x displayed uniformly across all modes
- **Hit rate**: 11% (safely above the 10% "non-paying" warning floor)
- **Volatility**: `High` for base, `Low` for freespins/bonus_buy
- **Sizes**: ~15 MiB total (~10 MiB math + ~5 MiB front) — visible upload
  progress without dragging the recording.

Math bundle layout:

```text
testdata/demo-bundle/math/
├── index.json                    # required by compliance, references all modes
├── base_weights.csv              # 120k entries, LUT for compliance
├── base_events.jsonl             # ~3 MiB of padded event stream
├── freespins_weights.csv         # 110k entries
├── freespins_events.jsonl        # ~2.5 MiB
├── bonus_buy_weights.csv         # 105k entries
├── bonus_buy_events.jsonl        # ~2 MiB
├── config.json                   # game config
├── symbols.json                  # symbol metadata
└── paytable.json                 # paytable
```

Front bundle layout:

```text
testdata/demo-bundle/front/
├── index.html                    # real HTML skeleton
├── manifest.json                 # real JSON referencing every asset
└── assets/
    ├── bundle.js                 # ~2 MiB padded JS
    ├── style.css                 # real CSS
    ├── img/logo.png              # valid 1x1 PNG
    ├── img/spritesheet.webp      # 500 KiB pseudo-random
    └── audio/
        ├── theme.mp3             # 1.8 MiB
        ├── spin.mp3               # 210 KiB
        └── win.mp3                # 310 KiB
```

## Scripted failures

The publish endpoint returns an error when the game slug ends with `-fail`:

```text
POST /file/publish/math  { "team": "neon-labs", "game": "cyber-samurai-fail" }
→ 200 { "code": "MATH_MISSING_MODE", "message": "missing weights file for mode 'bonus_buy'" }
```

Useful if you want to record a "publish fails → investigate → retry"
sequence.

## Tuning for the recording

All flags are on the mock API and the Makefile:

```bash
# Slower upload (more dramatic progress bar) — ~50 seconds for 15 MiB
./mockapi -throughput 500KiB

# Faster upload (tighter cut) — ~3 seconds
./mockapi -throughput 5MiB

# Customize via make
THROUGHPUT=1MiB make mockapi
```

Publish latency:

```bash
./mockapi -publish-delay 1200    # 1.2s artificial delay on /file/publish/*
```

## Environment variables reference

All demo targets set these automatically. Listed here for manual use:

```bash
export STAKE_API_URL=http://localhost:8080       # point at the mock
export STAKE_CONFIG_DIR=/tmp/stakecli-demo       # isolated config (not your real one)
export STAKE_KEYRING_DISABLE=1                   # no OS keychain writes
export STAKE_NO_UPDATE_CHECK=1                   # suppress background update check

# Optional — set ONLY if you want to skip the login screen:
export STAKE_SID=demo                            # CLI path auto-authenticates
```

`STAKE_SID` is checked by the CLI upload path (`GetActiveSID`) but NOT by
`auth.Init()`. So setting it bypasses auth for `stakecli upload` (and
`make demo-upload`) but doesn't affect TUI login. That's why `make demo-run`
deliberately leaves `STAKE_SID` unset.

## Makefile target cheat sheet

| Target | What it does |
| --- | --- |
| `make demo-build` | Build everything + generate bundle |
| `make mockapi` | Run the mock API (foreground) |
| `make demo-run` | Launch TUI with login screen every run |
| `make demo-fast` | Launch TUI with `STAKE_SID` bypass (skips login) |
| `make demo-upload` | Non-interactive CLI upload of the math bundle |
| `make demo-shell` | Spawn a shell with demo env vars exported |
| `make demo-reset` | Wipe mock's in-memory state |
| `make demo-logout` | Nuke the isolated demo config dir |
| `make demo-health` | Ping the mock's health endpoint |
| `make clean` | Remove binaries + bundle + demo config |

## What the mock does NOT do

- **Real S3**: PUT requests land on a throttled in-process handler, not a
  real bucket. That's the point — upload progress is deterministic.
- **Persistence**: all state is in-memory. Restart the mock or run
  `make demo-reset` to zero it.
- **Auth validation**: any non-empty `sid` cookie is accepted as "logged in".
- **Rate limiting / quotas**: no real-world constraints.

## Clean up

```bash
make clean              # removes binaries, testdata/demo-bundle/, /tmp/stakecli-demo/
```
