# Math compliance API

stakecli exposes math compliance as a versioned application API. The CLI,
TUI, MCP server, and a future graphical interface all consume the same
calculator and the same structured result.

The policy intentionally implements only the current compliance requirements.
Optimization preferences such as bonus frequency, session feel, payout shape,
and simulation-count recommendations are not compliance gates.

## API entry points

The application service exposes:

```go
policy := appService.CompliancePolicy()
result := appService.CheckMath(path)
result = appService.CheckMathWithOptions(path, upload.ComplianceOptions{
    BaseMode: "base",
})
```

The standalone CLI command uses the same calculator and does not require Stake
authentication or network access:

```bash
stakecli compliance .
stakecli compliance ./publish_files --base-mode base
stakecli compliance . --json
```

The MCP adapter exposes the same behavior through two local, read-only tools:

- `get_compliance_policy` returns the current hard and tier thresholds without
  inspecting a bundle;
- `check_math_compliance` returns the complete structured evaluation.

Example MCP arguments:

```json
{
  "path": "./publish_files",
  "compliance": {
    "base_mode": "base"
  }
}
```

`plan_upload`, `execute_upload`, and `start_upload` accept the same optional
`compliance` object. The values are included in the upload plan fingerprint,
so execution cannot silently use a different certification context than the
one reviewed during planning.

## Upload and publish acknowledgement

Path-based interactive math publication always displays compliance. A complete
passing result proceeds normally. Failed or otherwise incomplete compliance opens a
separate confirmation step asking whether the user is sure they want to
continue. The page lists the concrete failed, warning, and not-evaluated checks.
After explicit confirmation, both upload-only and upload-and-publish remain
available; compliance never becomes a permanent action block.
The game-detail publish action has no local bundle to evaluate, so it treats a
direct math publish as unchecked and requires its own explicit warning
confirmation.

Headless upload and publish skip compliance by default. Passing `--compliance`
prints the same advisory report before continuing. `--yes` is the explicit
acknowledgement for failed results. StakeCLI evaluates both tiers with the same
rules as `moptimizer/tools/lut_math_pdf_report.py`. Maximum configured platform
bet level and currency are excluded from the verdict. `base_mode` remains an
optional display hint and does not change the report-compatible checks.

MCP follows the same opt-in model through `check_compliance=true`. It requires
the local bundle `path`, recalculates advisory compliance and the remote
synchronization plan immediately before publishing, and treats the required
`confirm=true` as the explicit acknowledgement. Compliance issues do not block
publication. Pending upload, copy, or delete actions still stop publication
because scratch does not match the reviewed local bundle. Without
`check_compliance`, MCP publishes without a math check.

## Result contract

`ComplianceResult` contains:

- `schema_version` and the complete versioned `policy`;
- the report-compatible `method`, `scope`, and explicit `excluded_inputs`;
- overall `status`, `complete`, and `has_error` fields;
- stable checks with `code`, `scope`, `mode`, `tier`, `status`, `hard`, numeric
  bounds, actual value, and unit;
- per-mode RTP, volatility, hit rate, max-win odds, tail probabilities, CVaR,
  and ETL metrics;
- per-mode kind and RTP deviation from the rounded game mean;
- independent 2-star and 3-star eligibility plus the maximum compliant bet
  level calculated across every mode.

Statuses have distinct meanings:

- `pass`: the check or complete evaluation passes;
- `fail`: the value violates its bound;
- `warn`: the result is usable but contains a non-blocking data issue;
- `info`: a calculated or detected value;
- `not_evaluated`: a non-tier input could not be evaluated from the bundle.

`has_error` identifies hard incompatibilities in the report; it does not remove
the user's ability to upload or publish after acknowledgement. A failed 2-star
check does not fail a game that completely passes 3-star. A failed 3-star
threshold means no supported tier can accept the value, but remains advisory to
the transfer action. A calculated maximum compliant bet level is informational:
the unavailable platform bet level and currency never produce a PASS or FAIL.

## Input and normalization

Every weights CSV row is:

```text
sim_id,weight,payout
```

`payout` is an integer in hundredths of one base bet. For each mode:

```text
payoutBaseBet = payout / 100
normalizedPayout = payoutBaseBet / modeCost
RTP = weighted mean(normalizedPayout) × 100
```

Maximum payout and tail-risk tier checks use `normalizedPayout`, the return
relative to the amount paid for that mode. The response also retains
`max_payout_base_bet`, which is the raw maximum relative to one base bet.

The game-level tail probability, CVaR, and ETL values are the worst per-mode
values. A math bundle does not describe how often players select each bet mode,
so averaging modes would imply a selection distribution that is not present in
the source data. The conservative maximum makes the result deterministic and
ensures every mode remains within the selected tier.

CVaR is the weighted mean normalized payout in the largest 0.1% of outcomes.
The calculator handles a LUT row that spans only part of the 0.1% mass
fractionally. ETL values are calculated as RTP contribution shares:

```text
ETL40 = RTP contribution from normalized payouts >= 40x / total RTP contribution
ETL10000 = RTP contribution from normalized payouts >= 10,000x / total RTP contribution
ETLSum = ETL40 + ETL10000
```

## Hard limits

The following checks are blocking for every mode:

| Check | Limit |
| --- | ---: |
| RTP | 90.00%–96.70% |
| Actual max-win odds | no rarer than 1 in 20,000,000 |
| Non-zero hit probability | at least 5% |
| Cross-mode RTP deviation | ±0.50 percentage points |

Every mode is checked against the mean of all mode RTP values after each RTP is
rounded to 0.01 percentage points, matching the report generator:

```text
roundedModeRTP = round(modeRTP, 0.01 percentage points)
meanRTP = mean(roundedModeRTP for every mode)
abs(roundedModeRTP - meanRTP) <= 0.50 percentage points
```

The deviation is not calculated from a configured target, a selected base mode,
or the minimum-to-maximum spread.

The actual maximum positive LUT payout is used for achievability. A bundle with
no positive payout fails the check.

## Tier context and monetary metrics

The 2-star and 3-star policies include bet level, exposure, payout multiplier,
bet cost, cost multiplier, base volatility, 5,000x and 10,000x tails, CVaR, and
ETL limits. `get_compliance_policy` is the source of truth for the exact
versioned values.

For every mode and tier StakeCLI computes the maximum compatible bet level:

```text
maxByExposure = tierMaxExposure / modeMaxPayoutBaseBet
maxByCost = tierMaxBetCost / modeCost
modeMaxCompliantBetLevel =
    min(tierBetLevelMaximum, maxByExposure, maxByCost)
gameMaxCompliantBetLevel =
    min(modeMaxCompliantBetLevel for every mode)
```

Because the actual platform bet level and currency are not present in a math
bundle, Bet Level, Max Exposure, and Max Bet Cost are excluded from the tier
verdict. The calculated cap is reported explicitly instead. Max Payout
Multiplier is validated per mode on the absolute base-stake payout
(`raw payout / 100`); tail, CVaR, volatility, and ETL checks remain normalized
by mode cost.

## Base-mode selection

Base volatility follows the report generator and is evaluated for every mode
whose cost is exactly `1x`. The optional display base-mode selection order is:

1. the explicit `base_mode` option;
2. a mode named `base`, case-insensitively;
3. the mode with the lowest positive cost.

An explicit name that does not exist produces a warning and falls back to
automatic display selection; it does not change the compliance verdict.

## Bundle safety

`events` and `weights` references must remain inside the math bundle root.
Absolute or relative references that escape it are rejected, including
resolvable symlink escapes. LUT totals and per-payout weights are checked for
`uint64` overflow, malformed rows fail deterministically, and repeated
non-sequential-ID findings are aggregated into one warning instead of creating
an unbounded response.
