package main

import (
	"io"
	"net/http"
	"time"
)

// presignedURL builds the outward-facing URL a client should PUT to. The
// token is opaque — the server looks it up in state.presigned to resolve
// the upload target.
func (s *Server) presignedURL(r *http.Request, token string) string {
	// Prefer the explicit --public-url if provided (used when the mock runs
	// behind a tunnel / reverse proxy).
	if s.publicURL != "" {
		return s.publicURL + "/__s3/" + token
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host + "/__s3/" + token
}

// handleS3Put is the receiver for all "presigned" PUT uploads. It looks up
// the token, copies the body through a throughput-throttled reader (so the
// TUI progress bar moves visibly), and records the result in state.
//
// PUT /__s3/{token}
func (s *Server) handleS3Put(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	target, ok := s.state.ConsumePresigned(token)
	if !ok {
		http.Error(w, "unknown or already-used token", http.StatusNotFound)
		return
	}

	// Throttle the ingest so a 5 MiB file visibly takes ~1s at 5 MiB/s.
	// The throttle is a crude sleep-per-chunk loop — plenty for a demo.
	tr := &throttledReader{
		r:      r.Body,
		bps:    s.throughputBps,
		bucket: int64(s.chunkSize),
	}
	n, err := io.Copy(io.Discard, tr)
	_ = r.Body.Close()
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if target.Multipart != nil {
		// Nothing extra to record — the CompleteMultipartUpload call writes
		// the scratch entry using the aggregate session. But we still need
		// to return a successful 200 for the client to proceed.
		w.WriteHeader(http.StatusOK)
		return
	}

	etag := target.ExpectETag
	if etag == "" {
		etag = `"mockput-` + token[:8] + `"`
	}
	s.state.PutScratch(target.Team, target.Game, target.Path, etag, n)
	w.WriteHeader(http.StatusOK)
}

// ---------------------------------------------------------------------------
// throttledReader rate-limits an io.Reader to approximately bps bytes/sec.
//
// Implementation: a simple token bucket drained by wall-clock elapsed time.
// Accuracy is ~5 ms due to the sleep granularity, which is fine for a
// visual demo.
// ---------------------------------------------------------------------------

type throttledReader struct {
	r      io.Reader
	bps    int64 // bytes per second
	bucket int64 // max bytes to read per wakeup

	lastTick time.Time
	balance  int64
}

func (t *throttledReader) Read(p []byte) (int, error) {
	if t.bps <= 0 {
		return t.r.Read(p)
	}
	if t.lastTick.IsZero() {
		t.lastTick = time.Now()
		t.balance = t.bucket
	}

	for t.balance <= 0 {
		elapsed := time.Since(t.lastTick)
		refill := int64(float64(t.bps) * elapsed.Seconds())
		if refill <= 0 {
			// Wait a bit before trying again. 5 ms is a reasonable floor.
			time.Sleep(5 * time.Millisecond)
			continue
		}
		t.balance += refill
		t.lastTick = time.Now()
		if t.balance > t.bucket {
			t.balance = t.bucket
		}
	}

	n := min(int64(len(p)), t.balance)
	read, err := t.r.Read(p[:n])
	t.balance -= int64(read)
	return read, err
}
