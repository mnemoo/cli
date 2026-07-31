# MLR reweighting

`stakecli mlr` changes a headerless LUT CSV so its single heaviest row is no
more frequent than a requested `1 in N`. The operation is local and does not
require Stake Engine authentication.

## Input and invariants

Each input row must contain three unsigned integers:

```text
id,weight,payoutMultiplier
```

`payoutMultiplier` is expressed in cents, so `100` means `1.00x`. The command
preserves row count, order, IDs, and payouts. Only weights may change.

## Commands

The default operation chooses a donor automatically and always requests an RTP
lock:

```bash
stakecli mlr ./base.csv --target-n 250
stakecli mlr ./base.csv --target-n 250 --save
```

`inspect` is read-only. It shows the current MLR, violating payout values, and
candidate donor bands:

```bash
stakecli mlr inspect ./base.csv --target-n 250
```

`shift` applies a caller-selected donor strategy:

```bash
stakecli mlr shift ./base.csv \
  --target-n 250 \
  --donor 0-1.00 \
  --rtp-lock \
  --save
```

All modifying operations are dry runs unless `--save` is present. Saving uses
a temporary file in the LUT directory followed by an atomic rename.

## Algorithm

Let `W` be the original total weight and `N` the requested target. The per-row
cap is:

```text
cap = floor(W / N)
```

Every row above the cap is clamped to it. The removed weight is then deposited
into eligible rows without allowing any recipient to exceed the same cap.
This makes the final heaviest row approximately `W / N`; integer rounding can
produce a very small difference from the exact target.

Supported donor strategies are:

- `loss`: deposit into `0x` rows only.
- `spread`: deposit across every row with headroom.
- `<minX>-<maxX>`: deposit into an inclusive raw-payout range such as
  `0-1.00` or `1x-3x`.

The default operation previews a set of donor candidates, prefers candidates
that reach the target without overflow, and then chooses the smallest absolute
RTP change. If none can reach the target without overflow, it applies the
closest candidate as a best effort and reports the result as infeasible.

## RTP locking

Without a lock, changing where weight lives can change RTP. `--rtp-lock`
compensates by transferring weight between below-mean and above-mean payouts
while respecting the per-row cap. The global maximum-payout rows are excluded
from this correction so max-win frequency is preserved.

The command verifies the final drift instead of assuming the correction worked:

- `RTP lock: ✓ held` means absolute drift is below `0.01` percentage points.
- `RTP lock: ✗ NOT held` means the requested MLR and original RTP could not
  both be achieved with the available headroom.

The hard limit is structural: target MLR, exact RTP preservation, and a
reweight-only transformation cannot always be satisfied together. Lowering
`--target-n`, widening the donor range, or splitting dominant outcomes into
additional rows can restore feasibility.

## JSON output

Pass `--json` to any operation for machine-readable results:

```bash
stakecli mlr ./base.csv --target-n 250 --json
stakecli mlr inspect ./base.csv --target-n 250 --json
stakecli mlr shift ./base.csv --target-n 250 --donor spread --json
```

The default JSON payload includes the chosen donor, feasibility, whether the RTP
lock held, before/after MLR odds and RTP, leftover weight, and save status.

## MCP tools

- `mlr_inspect`: read current MLR, violating payouts, and donor candidates
- `mlr_preview_change`: calculate automatic or manual before/after metrics
  without writing
- `mlr_change`: atomically write and verify the change after `confirm=true`

MLR tools are local and do not require Stake authentication. They deliberately
do not use a preview fingerprint. A confirmed change creates a private sibling
backup before writing and restores the original if post-write verification
fails.
