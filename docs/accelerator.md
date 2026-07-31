# Encrypted upload accelerator

The Stake upload accelerator is an optional transport for presigned object
PUTs. It does not replace StakeCLI's upload engine.

StakeCLI remains responsible for:

- scanning the selected package;
- calculating S3-compatible ETags;
- comparing local files with Stake scratch state;
- excluding unchanged files;
- choosing upload, server-side copy, delete, and unchanged actions;
- initializing and completing multipart uploads;
- refreshing expired presigned URLs;
- publishing math or front-end versions;
- holding the Stake SID.

The accelerator receives only the body, size, content type, and short-lived
presigned URL for a part already selected by that plan. It never receives the
Stake SID.

## Install on Linux

The release installer supports systemd-based `amd64` and `arm64` Linux:

```bash
curl -fsSL \
  https://github.com/mnemoo/cli/releases/latest/download/install-accelerator.sh |
  sudo bash
```

Options:

```text
--port 7775
--version vX.Y.Z
--password-file /protected/path
```

The default interactive path reads and confirms the password through
`/dev/tty`, so it is not placed in shell history or process arguments. The
installer:

1. downloads the matching release archive;
2. verifies it against the release SHA-256 file;
3. creates a locked `stake-accelerator` system user;
4. generates a ten-year self-signed identity;
5. derives and stores a password-derived transport/authentication key;
6. installs a hardened systemd unit;
7. enables and starts the daemon.

The daemon listens on `:7775`. The installer intentionally does not change
UFW, firewalld, or a cloud security group.

## Pair and upload

```bash
stakecli accelerator add --name eu-relay 203.0.113.10:7775
stakecli accelerator list
stakecli accelerator speedtest --accelerator eu-relay --size 64

stakecli upload \
  --team TEAM \
  --game GAME \
  --type math \
  --path ./publish_files \
  --accelerator eu-relay
```

The first TLS certificate is saved only after the daemon proves knowledge of
the password-derived key by signing the client's one-time nonce. A TLS server
that merely returns a successful health response cannot become pinned. Later
certificate changes fail closed. No manual fingerprint argument is required.

An interactive `stakecli upload` with no transport flag asks whether to upload
directly, use a saved accelerator, or add a new one. Automation can make the
choice explicit with `--direct`, `--accelerator NAME`, or `--yes`.

The main StakeCLI TUI now uses the same route step inside every upload wizard:

1. choose **Direct to Stake**, a saved profile, or **Add a new accelerator**;
2. for a new relay, enter its IP/address, optional profile name, and password;
3. StakeCLI authenticates the daemon, pins its TLS identity, and saves the
   profile only after the password proof succeeds;
4. optionally run the encrypted 64 MiB speed test and review both
   client-to-relay and server receive/decrypt throughput;
5. use the verified relay for this upload or return to the connection list.

Passwords are masked, never written to the profile file, and cleared from the
form after each connection attempt. The confirmation screen shows the selected
upload route and allows returning to the route picker with `r`. A saved profile
named by `STAKE_ACCELERATOR` is preselected, and its environment password is
loaded only into the masked transient field.

Passwords can come from a hidden terminal prompt,
`STAKE_ACCELERATOR_PASSWORD`, or a mode `0600` file:

```bash
stakecli upload ... \
  --accelerator eu-relay \
  --accelerator-password-file /run/secrets/stake-accelerator
```

## Streaming and performance

StakeCLI keeps the existing multipart behavior: files above the multipart
threshold are represented by 8 MiB parts and up to 16 PUT bodies are scheduled
at once, with up to 32 outstanding operations. Relay intake uses four physical
HTTP/1.1 connections. HTTP/2 is intentionally disabled for this hop because
its concurrent streams would share one TCP connection and fail to accelerate
routes where a single flow cannot fill the link. The accelerator adapter
consumes the same `io.Reader`/`SectionReader` streams as direct upload.

Plaintext is never loaded as a whole file. Each stream is:

1. read in bounded 1 MiB buffers;
2. encrypted with hardware-accelerated AES-256-GCM frames;
3. sent over pinned TLS 1.3;
4. stored as ciphertext in the private spool;
5. decrypted from that spool directly into the remote PUT body.

The password KDF runs once while connecting, not for each byte or part.
Authenticated framing adds 20 bytes per 1 MiB plaintext frame, about 0.002%.

The CLI speedtest command and the TUI **Run 64 MiB speed test** action split an
encrypted generated payload into the same 8 MiB parts and send them over the
same four physical HTTP/1.1 connections, AES-GCM framing, and pinned TLS path
used by multipart relay intake. The result is aggregate throughput measured
over one shared wall-clock window, not the sum of unrelated per-request rates.
It reports both client-to-relay and daemon
receive/decrypt throughput. The test does not contact Stake or write a spool
file. Small files that Stake sends as one PUT can still be limited to a single
connection; the aggregate test represents large multipart books and batches.

`stakecli accelerator tasks` reports the two stages independently:

```text
intake=775.8MiB/s  remote=1048.7MiB/s
```

Local loopback numbers are diagnostic rather than a promise for a VPS. Actual
throughput is bounded by the client-to-relay route, relay disk/cache, relay CPU,
and relay-to-object-store route.

## Resume, background delivery, status, and cleanup

Every PUT part has a random resumable task ID. The encrypted stream is
deterministic only within that task, so after a broken client-to-relay
connection StakeCLI asks for the accepted ciphertext offset, rebuilds the
stream locally, skips the accepted prefix, and sends only the missing suffix.
The server never accepts an overlapping or mismatched offset.

Once the complete encrypted part is acknowledged, the daemon owns its delivery
and continues relay-to-Stake even if the client loses its connection or exits.
Retryable transport failures, HTTP 429 responses, and remote 5xx responses use
exponential backoff for up to one hour. Permanent failures and an expired
presigned URL fail immediately so StakeCLI can request a fresh URL on rerun.
The normal command keeps polling so it can complete Stake multipart metadata
and publish in the right order, but those polls are independent reconnectable
requests. If the process itself is stopped, rerun the same upload: StakeCLI's
normal remote-part/ETag planning discovers work the relay already completed
and does not resend matching data. The Stake SID is still never sent to the
relay.

An incomplete `receiving` session is retained while bytes continue to arrive.
After one hour without activity it and its partial ciphertext are deleted.
Committed queued tasks survive daemon restart and resume delivery
automatically.

```bash
stakecli accelerator tasks --accelerator eu-relay
stakecli accelerator tasks --accelerator eu-relay --watch
stakecli accelerator status --accelerator eu-relay TASK_ID
```

Statuses are `receiving`, `queued`, `uploading_to_stake`, `done`, `failed`, and
`cancelled`. The daemon exposes an authenticated server-sent event stream; the
current CLI task watcher polls once per second and reports live
`intake-now`/`remote-now` rates plus completed-stage averages.

Successful and failed payload spools are removed immediately. A minimal
credential-free status tombstone stays in memory for one hour, then disappears.

## Failure policy

The default is fail closed:

```bash
--accelerator-fallback fail
```

An explicit direct fallback is available:

```bash
--accelerator-fallback direct
```

If the relay fails after consuming a part stream, the normal StakeCLI retry
creates a fresh `SectionReader` for the same part and then switches to direct
PUT. Expired presigned URLs do not trigger direct fallback; StakeCLI first
refreshes them through the existing Stake API flow.

The same recovery layer also covers direct S3 transport failures such as
`request: EOF`. Each part receives up to 8 attempts with staggered exponential
backoff. If those attempts are exhausted, StakeCLI re-initializes and reconciles
the multipart upload up to 3 times; parts already marked complete by Stake are
not sent again. Permanent HTTP 4xx responses still fail immediately.

## Network boundary

Public operation accepts only HTTPS targets on port 443 whose DNS results are
public addresses. Redirects, URL credentials, loopback, private, link-local,
multicast, and metadata targets are rejected. Unauthenticated requests receive
no API information. Request nonces and timestamps prevent replay.

`--allow-private-targets` exists only on the daemon's `init` command for local
mock testing. The GitHub installer never enables it.

## CI/CD

Store the accelerator password in the CI secret manager:

```bash
export STAKE_ACCELERATOR=eu-relay
export STAKE_ACCELERATOR_PASSWORD="$ACCELERATOR_SECRET"
stakecli publish front ./dist \
  --headless --team TEAM --game GAME \
  --accelerator eu-relay
```

CI builds `stake-accelerator` as a static Linux binary for both `amd64` and
`arm64`. Tagged releases publish matching tarballs, checksums, the systemd unit,
and the standalone installer.
