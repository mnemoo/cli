// Command mockapi is a standalone HTTP server that implements the subset of
// the Stake Engine API that stakecli talks to. It serves hand-crafted
// fixtures for reads and tracks upload state in memory.
//
// Usage (from repo root):
//
//	go run ./cmd/mockapi                       # default :8080
//	go run ./cmd/mockapi -addr :9999
//	go run ./cmd/mockapi -throughput 2MiB      # slow upload for dramatic demo
//
// Point stakecli at it:
//
//	STAKE_SID=demo STAKE_API_URL=http://localhost:8080 ./stakecli
//
// Reset between recording takes:
//
//	curl -X POST http://localhost:8080/__reset
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

type Server struct {
	state *State

	// publicURL, if set, is used as the base for presigned URLs. Leave empty
	// to derive from the incoming request Host.
	publicURL string

	// throughputBps is the target ingest rate for presigned PUTs.
	throughputBps int64

	// chunkSize is the token bucket size for the throttled reader.
	chunkSize int

	// publishDelay is the artificial latency added to publish responses.
	publishDelay time.Duration
}

func main() {
	var (
		addr       = flag.String("addr", ":8080", "listen address")
		publicURL  = flag.String("public-url", "", "external base URL for presigned links (defaults to request Host)")
		throughput = flag.String("throughput", "3MiB", "upload throughput target (bytes per second; e.g. 500KiB, 2MiB)")
		publishMs  = flag.Int("publish-delay", 600, "artificial delay for POST /file/publish/* responses in ms")
	)
	flag.Parse()

	bps, err := parseSize(*throughput)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid -throughput: %v\n", err)
		os.Exit(2)
	}

	srv := &Server{
		state:         NewState(),
		publicURL:     strings.TrimRight(*publicURL, "/"),
		throughputBps: bps,
		chunkSize:     256 * 1024,
		publishDelay:  time.Duration(*publishMs) * time.Millisecond,
	}

	mux := http.NewServeMux()

	// --- Auth ---
	mux.HandleFunc("GET /users", srv.requireSID(srv.handleUsers))

	// --- Teams / games / stats (read-only) ---
	mux.HandleFunc("GET /teams", srv.requireSID(srv.handleTeams))
	mux.HandleFunc("GET /teams/{team}/games", srv.requireSID(srv.handleTeamGames))
	mux.HandleFunc("GET /teams/{team}/games/{game}", srv.requireSID(srv.handleTeamGameDetail))
	mux.HandleFunc("GET /teams/{team}/games/{game}/versions", srv.requireSID(srv.handleGameVersions))
	mux.HandleFunc("GET /teams/{team}/games/{game}/stats", srv.requireSID(srv.handleGameStats))
	mux.HandleFunc("GET /teams/{team}/balance", srv.requireSID(srv.handleTeamBalance))

	// --- File operations ---
	mux.HandleFunc("GET /file/game/{team}/{game}/scratch/{type}", srv.requireSID(srv.handleListScratch))
	mux.HandleFunc("POST /file/upload", srv.requireSID(srv.handleInitUpload))
	mux.HandleFunc("POST /file/multiupload", srv.requireSID(srv.handleInitMultiUpload))
	mux.HandleFunc("POST /file/complete", srv.requireSID(srv.handleCompleteUpload))
	mux.HandleFunc("POST /file/copy", srv.requireSID(srv.handleCopy))
	mux.HandleFunc("POST /file/multicopy", srv.requireSID(srv.handleMultiCopy))
	mux.HandleFunc("POST /file/delete", srv.requireSID(srv.handleDelete))
	mux.HandleFunc("POST /file/publish/{kind}", srv.requireSID(srv.handlePublish))

	// --- Presigned S3 receiver ---
	mux.HandleFunc("PUT /__s3/{token}", srv.handleS3Put)

	// --- Control ---
	mux.HandleFunc("POST /__reset", srv.handleReset)
	mux.HandleFunc("GET /__health", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})

	// Request logger wrapper — concise one-line format, stderr.
	handler := withLogging(mux)

	log.Printf("mockapi listening on %s (throughput=%s, publish-delay=%s)",
		*addr, *throughput, srv.publishDelay)
	log.Printf("point stakecli at it with: STAKE_SID=demo STAKE_API_URL=http://localhost%s", *addr)

	if err := http.ListenAndServe(*addr, handler); err != nil {
		log.Fatalf("listen: %v", err)
	}
}

// withLogging wraps h in a single-line access log. Skips the noisy PUT
// body — just logs method, path, status, and wall-clock duration.
func withLogging(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &statusRecorder{ResponseWriter: w, code: 200}
		h.ServeHTTP(rw, r)
		dur := time.Since(start)
		log.Printf("%3d %-6s %s (%s)", rw.code, r.Method, r.URL.Path, dur.Round(time.Millisecond))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.code = code
	r.ResponseWriter.WriteHeader(code)
}

// sleep is a context-free delay used for publish responses. Kept as a
// method so tests can override it in the future if we add any.
func (s *Server) sleep(d time.Duration) {
	if d > 0 {
		time.Sleep(d)
	}
}

// parseSize accepts a human-readable byte count like "500KiB", "2MiB",
// "3145728", or "5MB". Returns bytes.
func parseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty")
	}

	// Separate number from suffix.
	i := 0
	for i < len(s) && (s[i] == '.' || (s[i] >= '0' && s[i] <= '9')) {
		i++
	}
	numStr := s[:i]
	suffix := strings.TrimSpace(s[i:])

	n, err := strconv.ParseFloat(numStr, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid number %q", numStr)
	}

	var mult float64
	switch strings.ToLower(suffix) {
	case "", "b":
		mult = 1
	case "k", "kb":
		mult = 1000
	case "kib":
		mult = 1024
	case "m", "mb":
		mult = 1000 * 1000
	case "mib":
		mult = 1024 * 1024
	case "g", "gb":
		mult = 1000 * 1000 * 1000
	case "gib":
		mult = 1024 * 1024 * 1024
	default:
		return 0, fmt.Errorf("unknown suffix %q", suffix)
	}

	return int64(n * mult), nil
}
