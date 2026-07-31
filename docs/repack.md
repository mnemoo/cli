# Zstd book repack estimator

StakeCLI can estimate whether existing `books*.jsonl.zst` files would become
meaningfully smaller at Zstd level 10:

```bash
stakecli repack estimate --level 10 --sample-mib 64 ./publish_files
```

The estimator is read-only. It never replaces a book, changes an ETag, creates
a second bundle, or contacts Stake.

For every matching book it:

1. streams decompression through a bounded reader;
2. samples up to 64 MiB of uncompressed JSONL;
3. recompresses that sample with the Go Zstd level closest to level 10 using
   an automatically selected number of CPU workers;
4. compares the old compressed bytes consumed with the new sample;
5. extrapolates size and repack time for a large book.

When the entire book fits in the sample, the displayed size is marked `exact`.
Larger books are explicitly marked `sampled estimate`. The 32 KiB compressed
read cap limits decoder read-ahead so it does not distort the estimate
materially.

StakeCLI recommends repacking only when one book is expected to save both:

- at least 5% of its current compressed size; and
- at least 16 MiB.

The regular CLI upload never runs this CPU-heavy analysis automatically. The
TUI first performs only a cheap size check. When matching books total at least
100 MiB, it asks whether to analyze them; declining continues immediately.
`--estimate-repack` explicitly prints the same read-only report in CLI/headless
workflows.

When the estimate finds a worthwhile difference, the upload confirmation
screen shows **Repack books for this upload**. Before it starts, the screen
displays the system temporary directory, a conservative temporary-space
estimate, the selected worker count, and the corresponding working-memory
allowance. The action is reversible: **Use original books instead** deletes
staging and rebuilds the original upload plan.

JSON output is available for tooling:

```bash
stakecli repack estimate --json ./publish_files
```

## Verified CI repack

`--repack` is the explicit automation flag:

```bash
stakecli upload \
  --team TEAM --game GAME --type math --path ./publish_files \
  --yes --repack

stakecli publish math ./publish_files \
  --headless --team TEAM --game GAME --repack
```

It first estimates every book and attempts a full repack only for candidates
above the 5% and 16 MiB thresholds. The actual result must also pass both
thresholds. Otherwise the original hard-linked book remains in the upload
bundle.

Repacked bytes are written to a private mirror under the operating system's
standard temporary directory: `$TMPDIR`/`/tmp` on Unix-like systems and
`%TEMP%` on Windows. StakeCLI streams decompressed JSONL directly into the
level-10 encoder; it never materializes plaintext JSON files. It then
decompresses the new stream again and requires its uncompressed SHA-256 to
match the original before it can be uploaded. Original files are never opened
for writing. Temporary staging is removed after upload, cancellation, normal
TUI exit, or immediately when no actual improvement is found. Directories left
by a hard crash or power loss are removed by a later repack after a 24-hour
safety window.

The estimator and full repack automatically use up to 8 CPU workers, limited by
the CPU budget available to the process. A single large book can therefore use
multiple cores. Books themselves remain sequential so several large encoders
cannot multiply memory usage without a bound. During a TUI repack, StakeCLI
shows the current book, compression or SHA-256 verification phase, compressed
bytes, percentage, worker count, and elapsed time.

## Resource warning

Repack is intentionally opt-in and is not recommended for ordinary uploads:

- Zstd level 10 can consume substantial CPU and memory;
- the encoder automatically uses up to 8 workers; the TUI shows a conservative
  RAM allowance calculated for the selected worker count before starting;
- verification requires a second full decompression pass;
- the job may take longer than the network time it saves on a fast connection;
- a temporary copy of each improved compressed book needs disk space;
- if the system temporary directory is RAM-backed, temporary-space use also
  consumes RAM;
- already well-packed books usually gain too little and are left unchanged.

It is most useful in CI/CD when very large books were produced with a fast
compression level and the upload route is slow or unreliable. Use the
read-only estimator first when unsure.
