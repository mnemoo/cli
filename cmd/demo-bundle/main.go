// Command demo-bundle generates a deterministic math + front bundle for
// the mockapi demo recording. The output is sized for a visually pleasing
// upload (~25 MiB across ~15 files) and passes stakecli's math compliance
// check with realistic slot-game RTP numbers.
//
// Usage:
//
//	go run ./cmd/demo-bundle                          # writes to testdata/demo-bundle
//	go run ./cmd/demo-bundle -out /tmp/bundle
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
)

// -----------------------------------------------------------------------------
// Math bundle shape.
// -----------------------------------------------------------------------------

type modeSpec struct {
	Name    string  `json:"name"`
	Cost    float64 `json:"cost"`
	Events  string  `json:"events"`
	Weights string  `json:"weights"`
}

type indexFile struct {
	Game        string     `json:"game"`
	Version     int        `json:"version"`
	GeneratedAt string     `json:"generated_at"`
	Modes       []modeSpec `json:"modes"`
}

type modePlan struct {
	name      string
	cost      float64
	targetRTP float64 // expressed as percent (e.g. 96.42)
	lines     int     // number of LUT entries (>= 100_000 to pass compliance)
	seed      int64
}

// Three modes with cross-mode RTP variation ~0.14% (<0.5% limit).
var modePlans = []modePlan{
	{name: "base", cost: 1.0, targetRTP: 96.42, lines: 120_000, seed: 42},
	{name: "freespins", cost: 100.0, targetRTP: 96.38, lines: 110_000, seed: 1337},
	{name: "bonus_buy", cost: 200.0, targetRTP: 96.28, lines: 105_000, seed: 2024},
}

// -----------------------------------------------------------------------------
// Entry point.
// -----------------------------------------------------------------------------

func main() {
	out := flag.String("out", "testdata/demo-bundle", "output directory (will be created)")
	flag.Parse()

	if err := os.MkdirAll(*out, 0o755); err != nil {
		log.Fatalf("mkdir %s: %v", *out, err)
	}
	mathDir := filepath.Join(*out, "math")
	frontDir := filepath.Join(*out, "front")

	if err := os.RemoveAll(mathDir); err != nil {
		log.Fatalf("rm math: %v", err)
	}
	if err := os.RemoveAll(frontDir); err != nil {
		log.Fatalf("rm front: %v", err)
	}

	log.Printf("generating math bundle at %s", mathDir)
	if err := generateMath(mathDir); err != nil {
		log.Fatalf("math: %v", err)
	}

	log.Printf("generating front bundle at %s", frontDir)
	if err := generateFront(frontDir); err != nil {
		log.Fatalf("front: %v", err)
	}

	log.Printf("demo bundle ready at %s", *out)
}

// =============================================================================
// Math bundle.
// =============================================================================

func generateMath(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	// 1) Weights CSVs (these drive the compliance RTP calculation).
	for _, m := range modePlans {
		csvPath := filepath.Join(dir, m.name+"_weights.csv")
		if err := writeWeightsCSV(csvPath, m); err != nil {
			return fmt.Errorf("weights for %s: %w", m.name, err)
		}
	}

	// 2) Events files — compliance only checks existence, but scanner
	//    uploads the bytes, so we pad to a meaningful size.
	for _, m := range modePlans {
		eventsPath := filepath.Join(dir, m.name+"_events.jsonl")
		size := map[string]int{
			"base":      3_200_000,
			"freespins": 2_400_000,
			"bonus_buy": 1_800_000,
		}[m.name]
		if err := writeFakeEvents(eventsPath, m.name, size, m.seed); err != nil {
			return fmt.Errorf("events for %s: %w", m.name, err)
		}
	}

	// 3) index.json — required by compliance, references each mode.
	idx := indexFile{
		Game:        "cyber-samurai",
		Version:     1,
		GeneratedAt: "2026-04-09T10:00:00Z",
	}
	for _, m := range modePlans {
		idx.Modes = append(idx.Modes, modeSpec{
			Name:    m.name,
			Cost:    m.cost,
			Events:  m.name + "_events.jsonl",
			Weights: m.name + "_weights.csv",
		})
	}
	if err := writeJSONFile(filepath.Join(dir, "index.json"), idx); err != nil {
		return err
	}

	// 4) Companion metadata — not checked by compliance but typical of a real bundle.
	if err := writeJSONFile(filepath.Join(dir, "config.json"), map[string]any{
		"game":     "cyber-samurai",
		"version":  1,
		"reels":    []int{5, 3},
		"paylines": 20,
		"minBet":   0.20,
		"maxBet":   100,
		"features": []string{"free_spins", "bonus_buy", "wild_substitution", "scatter"},
	}); err != nil {
		return err
	}
	if err := writeJSONFile(filepath.Join(dir, "symbols.json"), buildSymbols()); err != nil {
		return err
	}
	if err := writeJSONFile(filepath.Join(dir, "paytable.json"), buildPaytable()); err != nil {
		return err
	}

	return nil
}

// writeWeightsCSV emits a CSV file with (sim_id,weight,payout) rows whose
// weighted average payout hits the target RTP exactly.
//
// Distribution: ~92% zero-payout spins, ~8% winners uniformly distributed
// around the target mean, plus one 1000x jackpot entry and one balancing
// entry that absorbs RNG rounding error to lock the total to the target.
// This gives compliance a "Max win: 1000.00x" line with a realistic hit
// rate and volatility — much more impressive on camera than the flat
// distribution that would otherwise fall out of pure uniform RNG.
//
// RTP calculation (matches internal/upload/compliance.go):
//
//	RTP% = weightedPayout / totalWeight / 100 / cost * 100
//	     = weightedPayout / (totalWeight * cost)
//
// so weightedPayout = RTP% * totalWeight * cost (with weight=1 per row).
func writeWeightsCSV(path string, m modePlan) error {
	target := int64(m.targetRTP * float64(m.lines) * m.cost)
	if target <= 0 {
		return fmt.Errorf("non-positive target for mode %q", m.name)
	}

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	bw := bufio.NewWriterSize(f, 1<<16)
	rng := rand.New(rand.NewSource(m.seed))

	// The compliance report formats max win as `payout / 100` regardless of
	// mode cost, so a fixed 500_000 payout always displays as "5000.00x"
	// across all modes — consistent and demo-friendly.
	const jackpotPayout = int64(500_000)

	// 89% zero-payout spins keeps the demo below the 90% "non-paying" warning
	// threshold while still looking like a realistic slot hit rate (~11%).
	zeroCount := int(float64(m.lines-1) * 0.89)
	winnerCount := m.lines - 1 - zeroCount // -1 reserves the jackpot slot

	expectedWinnerTotal := target - jackpotPayout
	if expectedWinnerTotal < 0 || winnerCount <= 0 {
		return fmt.Errorf("invalid mode plan for %q (target=%d winners=%d)", m.name, target, winnerCount)
	}

	// Exact integer split: winnerCount winners whose sum is expectedWinnerTotal
	// to the unit. (winnerCount - r) winners get q, r winners get q+1.
	q := expectedWinnerTotal / int64(winnerCount)
	r := expectedWinnerTotal - q*int64(winnerCount)

	payouts := make([]int64, m.lines)

	// Winners placed sequentially — they'll be shuffled below.
	for i := range winnerCount {
		payout := q
		if int64(i) < r {
			payout = q + 1
		}
		payouts[zeroCount+i] = payout
	}

	// Jackpot slot at the end (pre-shuffle).
	payouts[m.lines-1] = jackpotPayout

	// Shuffle so zeros aren't clumped at the start.
	rng.Shuffle(m.lines, func(i, j int) {
		payouts[i], payouts[j] = payouts[j], payouts[i]
	})

	for i, p := range payouts {
		if _, err := fmt.Fprintf(bw, "%d,1,%d\n", i, p); err != nil {
			return err
		}
	}
	return bw.Flush()
}

// writeFakeEvents emits a JSONL-ish file padded to the requested size. The
// first few lines are parseable JSON so a human glancing at the file in a
// hex editor sees structured data; the rest is filler.
func writeFakeEvents(path, mode string, targetSize int, seed int64) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	bw := bufio.NewWriterSize(f, 1<<16)

	// A handful of real-looking JSONL lines at the top.
	real := []string{
		fmt.Sprintf(`{"mode":%q,"spin":1,"reels":[[11,3,7],[2,1,9],[6,4,8],[5,12,0],[10,2,6]],"payout":0}`, mode),
		fmt.Sprintf(`{"mode":%q,"spin":2,"reels":[[4,7,1],[11,8,3],[5,0,12],[9,6,2],[1,10,4]],"payout":120}`, mode),
		fmt.Sprintf(`{"mode":%q,"spin":3,"reels":[[7,2,9],[4,6,11],[0,5,8],[3,12,1],[10,7,6]],"payout":0}`, mode),
		fmt.Sprintf(`{"mode":%q,"spin":4,"reels":[[9,11,5],[1,3,7],[12,0,10],[8,6,2],[4,9,11]],"payout":480}`, mode),
		fmt.Sprintf(`{"mode":%q,"spin":5,"reels":[[6,8,0],[5,2,9],[1,10,4],[7,11,3],[12,5,8]],"payout":0}`, mode),
	}
	for _, line := range real {
		_, _ = bw.WriteString(line)
		_ = bw.WriteByte('\n')
	}

	// Deterministic filler so the file is reproducible and chunked.
	rng := rand.New(rand.NewSource(seed))
	buf := make([]byte, 4096)
	for i := range buf {
		buf[i] = byte('A' + rng.Intn(26))
	}

	written := 0
	for _, line := range real {
		written += len(line) + 1
	}
	for written < targetSize {
		n := min(len(buf), targetSize-written)
		nw, err := bw.Write(buf[:n])
		if err != nil {
			return err
		}
		written += nw
	}
	return bw.Flush()
}

// =============================================================================
// Front bundle.
// =============================================================================

func generateFront(dir string) error {
	if err := os.MkdirAll(filepath.Join(dir, "assets", "audio"), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, "assets", "img"), 0o755); err != nil {
		return err
	}

	// index.html — small, real HTML.
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(indexHTML), 0o644); err != nil {
		return err
	}

	// Minimal CSS.
	if err := os.WriteFile(filepath.Join(dir, "assets", "style.css"), []byte(stylesCSS), 0o644); err != nil {
		return err
	}

	// manifest.json — real JSON that references everything else.
	if err := writeJSONFile(filepath.Join(dir, "manifest.json"), map[string]any{
		"game":    "cyber-samurai",
		"version": "1.0.0",
		"entry":   "index.html",
		"assets": []string{
			"assets/bundle.js",
			"assets/style.css",
			"assets/img/spritesheet.webp",
			"assets/img/logo.png",
			"assets/audio/theme.mp3",
			"assets/audio/spin.mp3",
			"assets/audio/win.mp3",
		},
	}); err != nil {
		return err
	}

	// bundle.js — starts with real-looking code, filled to ~2 MiB.
	jsHeader := `// Cyber Samurai — production bundle (demo)
(function () {
  'use strict';
  const GameState = { mode: 'base', balance: 1000, bet: 1, spins: 0 };
  const Reels = [5, 3];
  const Symbols = ['WILD','SCATTER','KATANA','OBI','LANTERN','KOI','NEON','YEN'];
  function init(cfg) { /* bootstrap */ return Object.assign({}, GameState, cfg); }
  function spin(state) { state.spins++; return state; }
  function render(state) { /* draw */ return state; }
  module.exports = { init, spin, render };
})();
`
	if err := writeFilledFile(filepath.Join(dir, "assets", "bundle.js"), jsHeader, 2_100_000, "// fill\n", 101); err != nil {
		return err
	}

	// logo.png — a tiny valid 1x1 PNG (red pixel), padded slightly so
	// scanner picks up something non-empty.
	redPNG := []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A,
		0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53,
		0xDE, 0x00, 0x00, 0x00, 0x0C, 0x49, 0x44, 0x41,
		0x54, 0x08, 0x99, 0x63, 0xF8, 0xCF, 0xC0, 0x00,
		0x00, 0x00, 0x03, 0x00, 0x01, 0x5B, 0xE0, 0xAF,
		0x5A, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4E,
		0x44, 0xAE, 0x42, 0x60, 0x82,
	}
	if err := os.WriteFile(filepath.Join(dir, "assets", "img", "logo.png"), redPNG, 0o644); err != nil {
		return err
	}

	// spritesheet.webp — pseudo-random bytes. NOT a valid WebP; the scanner
	// doesn't care and the mock only uploads the bytes.
	if err := writeRandomFile(filepath.Join(dir, "assets", "img", "spritesheet.webp"), 520_000, 7); err != nil {
		return err
	}

	// Audio files: fake bytes sized to resemble real MP3 durations.
	if err := writeRandomFile(filepath.Join(dir, "assets", "audio", "theme.mp3"), 1_800_000, 11); err != nil {
		return err
	}
	if err := writeRandomFile(filepath.Join(dir, "assets", "audio", "spin.mp3"), 210_000, 13); err != nil {
		return err
	}
	if err := writeRandomFile(filepath.Join(dir, "assets", "audio", "win.mp3"), 310_000, 17); err != nil {
		return err
	}

	return nil
}

// =============================================================================
// Small helpers.
// =============================================================================

func writeJSONFile(path string, v any) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func writeFilledFile(path, header string, totalSize int, filler string, seed int64) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	bw := bufio.NewWriterSize(f, 1<<16)
	if _, err := bw.WriteString(header); err != nil {
		return err
	}
	written := len(header)
	rng := rand.New(rand.NewSource(seed))
	var sb strings.Builder
	for written < totalSize {
		sb.Reset()
		for range 20 {
			_, _ = sb.WriteString(filler)
			_, _ = fmt.Fprintf(&sb, "var _%d=%d;\n", rng.Intn(1_000_000), rng.Intn(1_000_000))
		}
		chunk := sb.String()
		if written+len(chunk) > totalSize {
			chunk = chunk[:totalSize-written]
		}
		nw, err := bw.WriteString(chunk)
		if err != nil {
			return err
		}
		written += nw
	}
	return bw.Flush()
}

func writeRandomFile(path string, size int, seed int64) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	bw := bufio.NewWriterSize(f, 1<<16)
	rng := rand.New(rand.NewSource(seed))
	buf := make([]byte, 4096)
	written := 0
	for written < size {
		for i := range buf {
			buf[i] = byte(rng.Intn(256))
		}
		n := min(len(buf), size-written)
		if _, err := bw.Write(buf[:n]); err != nil {
			return err
		}
		written += n
	}
	return bw.Flush()
}

// -----------------------------------------------------------------------------
// Static content for front bundle (kept small).
// -----------------------------------------------------------------------------

const indexHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>Cyber Samurai</title>
  <link rel="stylesheet" href="assets/style.css">
</head>
<body>
  <div id="game-root" data-game="cyber-samurai"></div>
  <script src="assets/bundle.js"></script>
</body>
</html>
`

const stylesCSS = `:root {
  --bg: #0a0e1a;
  --accent: #ff2d75;
  --text: #f0f4ff;
}
html, body {
  margin: 0;
  padding: 0;
  height: 100%;
  background: var(--bg);
  color: var(--text);
  font-family: 'Inter', -apple-system, sans-serif;
}
#game-root {
  display: flex;
  align-items: center;
  justify-content: center;
  min-height: 100vh;
  background-image: radial-gradient(circle at 50% 30%, rgba(255,45,117,0.15), transparent);
}
`

func buildSymbols() any {
	return map[string]any{
		"WILD":    map[string]any{"id": 0, "stacks": true},
		"SCATTER": map[string]any{"id": 1, "triggers": "free_spins"},
		"KATANA":  map[string]any{"id": 2, "tier": "high"},
		"OBI":     map[string]any{"id": 3, "tier": "high"},
		"LANTERN": map[string]any{"id": 4, "tier": "mid"},
		"KOI":     map[string]any{"id": 5, "tier": "mid"},
		"NEON":    map[string]any{"id": 6, "tier": "low"},
		"YEN":     map[string]any{"id": 7, "tier": "low"},
	}
}

func buildPaytable() any {
	return map[string]any{
		"WILD":    []int{0, 0, 50, 150, 500},
		"KATANA":  []int{0, 0, 40, 120, 400},
		"OBI":     []int{0, 0, 30, 90, 250},
		"LANTERN": []int{0, 0, 20, 60, 150},
		"KOI":     []int{0, 0, 15, 40, 100},
		"NEON":    []int{0, 0, 10, 25, 60},
		"YEN":     []int{0, 0, 5, 15, 40},
	}
}
