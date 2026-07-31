package main

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mnemoo/cli/internal/api"
)

// ---------------------------------------------------------------------------
// Auth middleware — accepts any non-empty `sid` cookie as "logged in".
// ---------------------------------------------------------------------------

func (s *Server) requireSID(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("sid")
		if err != nil || c.Value == "" {
			writeErr(w, http.StatusUnauthorized, "missing sid cookie")
			return
		}
		next(w, r)
	}
}

// ---------------------------------------------------------------------------
// Read endpoints.
// ---------------------------------------------------------------------------

// GET /users
func (s *Server) handleUsers(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, demoUser)
}

// GET /teams
func (s *Server) handleTeams(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, teams)
}

// GET /teams/{team}/games
func (s *Server) handleTeamGames(w http.ResponseWriter, r *http.Request) {
	team := r.PathValue("team")
	games, ok := gamesByTeam[team]
	if !ok {
		writeJSON(w, http.StatusOK, []api.TeamGameCard{})
		return
	}
	writeJSON(w, http.StatusOK, games)
}

// GET /teams/{team}/games/{game}
func (s *Server) handleTeamGameDetail(w http.ResponseWriter, r *http.Request) {
	team := r.PathValue("team")
	slug := r.PathValue("game")
	games, ok := gamesByTeam[team]
	if !ok {
		writeErr(w, http.StatusNotFound, "team not found")
		return
	}
	for _, g := range games {
		if g.Slug == slug {
			detail := api.TeamGameDetail{
				Name:          g.Name,
				Slug:          g.Slug,
				Image:         g.Image,
				Rating:        g.Rating,
				Approval:      g.Approval,
				OnlinePlayers: g.OnlinePlayers,
			}
			writeJSON(w, http.StatusOK, detail)
			return
		}
	}
	writeErr(w, http.StatusNotFound, "game not found")
}

// GET /teams/{team}/games/{game}/versions
func (s *Server) handleGameVersions(w http.ResponseWriter, r *http.Request) {
	team := r.PathValue("team")
	slug := r.PathValue("game")
	versions := gameVersions[team+"/"+slug]
	if versions == nil {
		versions = []api.GameVersionHistoryItem{}
	}
	writeJSON(w, http.StatusOK, versions)
}

// GET /teams/{team}/balance
func (s *Server) handleTeamBalance(w http.ResponseWriter, r *http.Request) {
	team := r.PathValue("team")
	b, ok := balances[team]
	if !ok {
		writeErr(w, http.StatusNotFound, "team not found")
		return
	}
	writeJSON(w, http.StatusOK, b)
}

// GET /teams/{team}/teamStats
func (s *Server) handleTeamStats(w http.ResponseWriter, r *http.Request) {
	stats, ok := teamOperatorStats[r.PathValue("team")]
	if !ok {
		writeErr(w, http.StatusNotFound, "team not found")
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

// GET /teams/{team}/payments/method
func (s *Server) handlePaymentMethod(w http.ResponseWriter, r *http.Request) {
	method, ok := paymentMethods[r.PathValue("team")]
	if !ok {
		writeErr(w, http.StatusNotFound, "team not found")
		return
	}
	writeJSON(w, http.StatusOK, method)
}

// GET /teams/{team}/games/{game}/stats
func (s *Server) handleGameStats(w http.ResponseWriter, r *http.Request) {
	team := r.PathValue("team")
	slug := r.PathValue("game")
	stats, ok := gameStats[team+"/"+slug]
	if !ok {
		// Unknown games get an empty stats response (valid shape).
		stats = api.GameStatsByModeResponse{
			Name:  slug,
			Slug:  slug,
			Stats: []api.GameModeStat{},
		}
	}
	writeJSON(w, http.StatusOK, scaleGameStats(stats, requestedRangeScale(r)))
}

// GET /teams/{team}/graph?game=...&start=...&end=...
func (s *Server) handleGameGraph(w http.ResponseWriter, r *http.Request) {
	team := r.PathValue("team")
	game := r.URL.Query().Get("game")
	stats, ok := gameStats[team+"/"+game]
	if !ok || len(stats.Stats) == 0 {
		writeJSON(w, http.StatusOK, api.StatsGraphResponse{})
		return
	}

	end := time.Now().Unix()
	start := end - int64(24*time.Hour/time.Second)
	if parsed, err := strconv.ParseInt(r.URL.Query().Get("start"), 10, 64); err == nil {
		start = parsed
	}
	if parsed, err := strconv.ParseInt(r.URL.Query().Get("end"), 10, 64); err == nil {
		end = parsed
	}
	if end <= start {
		writeErr(w, http.StatusBadRequest, "end must be after start")
		return
	}

	points := int((end - start) / int64(30*time.Minute/time.Second))
	if points < 1 {
		points = 1
	}
	if points > 1_500 {
		points = 1_500
	}
	var totalTurnover, totalProfit, totalCount float64
	for _, stat := range stats.Stats {
		totalTurnover += stat.Turnover
		totalProfit += stat.Profit
		totalCount += float64(stat.Count)
	}
	actualEdge := 0.04
	if totalTurnover > 0 {
		actualEdge = totalProfit / totalTurnover
	}
	scale := requestedRangeScale(r)
	totalTurnover *= scale
	totalCount *= scale
	bucketSeconds := float64(end-start) / float64(points)

	graph := api.StatsGraphResponse{
		Buckets: make([]string, points), Turnover: make([]float64, points),
		Profit: make([]float64, points), Count: make([]float64, points),
	}
	for i := 0; i < points; i++ {
		wave := 1 + 0.35*math.Sin(float64(i)*0.53) + 0.15*math.Cos(float64(i)*0.17)
		turnover := totalTurnover / float64(points) * wave
		profitWave := actualEdge + 0.018*math.Sin(float64(i)*0.79)
		graph.Buckets[i] = strconv.FormatInt(start+int64(float64(i)*bucketSeconds), 10)
		graph.Turnover[i] = turnover
		graph.Profit[i] = turnover * profitWave
		graph.Count[i] = math.Max(1, totalCount/float64(points)*wave)
	}
	writeJSON(w, http.StatusOK, graph)
}

func requestedRangeScale(r *http.Request) float64 {
	start, startErr := strconv.ParseInt(r.URL.Query().Get("start"), 10, 64)
	end, endErr := strconv.ParseInt(r.URL.Query().Get("end"), 10, 64)
	if startErr != nil || endErr != nil || end <= start {
		return 1
	}
	scale := float64(end-start) / float64(int64(30*24*time.Hour/time.Second))
	if scale < 0.001 {
		return 0.001
	}
	return scale
}

func scaleGameStats(stats api.GameStatsByModeResponse, scale float64) api.GameStatsByModeResponse {
	if scale == 1 {
		return stats
	}
	result := stats
	result.Stats = make([]api.GameModeStat, len(stats.Stats))
	for i, stat := range stats.Stats {
		stat.Count = int64(math.Round(float64(stat.Count) * scale))
		stat.Turnover *= scale
		stat.Profit *= scale
		stat.ExpectedReturn *= scale
		result.Stats[i] = stat
	}
	return result
}

// ---------------------------------------------------------------------------
// Scratch listing.
// ---------------------------------------------------------------------------

// GET /file/game/{team}/{game}/scratch/{type}
func (s *Server) handleListScratch(w http.ResponseWriter, r *http.Request) {
	team := r.PathValue("team")
	game := r.PathValue("game")
	uploadType := r.PathValue("type")
	objects := s.state.ListScratch(team, game, uploadType)
	writeJSON(w, http.StatusOK, objects)
}

// ---------------------------------------------------------------------------
// Upload — single part.
// ---------------------------------------------------------------------------

// POST /file/upload
func (s *Server) handleInitUpload(w http.ResponseWriter, r *http.Request) {
	var body api.FileUploadBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	token := s.state.RegisterPresigned(&presignedTarget{
		Team:       body.Team,
		Game:       body.Game,
		Path:       body.Path,
		Key:        body.Team + "/" + body.Game + body.Path,
		ExpectETag: body.ETag,
	})
	writeJSON(w, http.StatusOK, api.UploadResponse{
		Key: body.Team + "/" + body.Game + body.Path,
		URL: s.presignedURL(r, token),
	})
}

// ---------------------------------------------------------------------------
// Upload — multipart.
// ---------------------------------------------------------------------------

// POST /file/multiupload
func (s *Server) handleInitMultiUpload(w http.ResponseWriter, r *http.Request) {
	var body api.FileMultiUploadBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}

	session := &multipartSession{
		Team:     body.Team,
		Game:     body.Game,
		Path:     body.Path,
		Key:      body.Team + "/" + body.Game + body.Path,
		Filename: body.Filename,
		Size:     body.Size,
	}
	uploadID := s.state.RegisterMultipart(session)

	parts := make([]api.MultiUploadPartResponse, len(body.Parts))
	for i, p := range body.Parts {
		token := s.state.RegisterPresigned(&presignedTarget{
			Team: body.Team,
			Game: body.Game,
			Path: body.Path,
			Key:  session.Key,
			Multipart: &multipartContext{
				UploadID: uploadID,
				Part:     p.Number,
			},
		})
		parts[i] = api.MultiUploadPartResponse{
			URL:    s.presignedURL(r, token),
			Size:   p.Size,
			Number: p.Number,
		}
	}
	session.Parts = parts

	writeJSON(w, http.StatusOK, api.MultiUploadInitResponse{
		Key:      session.Key,
		UploadID: uploadID,
		Parts:    parts,
	})
}

// POST /file/complete
func (s *Server) handleCompleteUpload(w http.ResponseWriter, r *http.Request) {
	var body api.FileCompleteBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	session, ok := s.state.CompleteMultipart(body.UploadID)
	if !ok {
		writeErr(w, http.StatusNotFound, "upload session not found")
		return
	}
	etag, err := multipartETag(body.Parts)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid multipart etag: "+err.Error())
		return
	}
	s.state.PutScratch(session.Team, session.Game, session.Path, etag, session.Size)

	writeJSON(w, http.StatusOK, api.CompleteResponse{ETag: etag})
}

func multipartETag(parts []api.FileCompletePartBody) (string, error) {
	if len(parts) == 0 {
		return "", fmt.Errorf("parts are required")
	}
	composite := md5.New()
	for _, part := range parts {
		value := strings.Trim(part.ETag, `"`)
		digest, err := hex.DecodeString(value)
		if err != nil || len(digest) != md5.Size {
			return "", fmt.Errorf("part %d has invalid etag", part.Number)
		}
		_, _ = composite.Write(digest)
	}
	return fmt.Sprintf(`"%s-%d"`, hex.EncodeToString(composite.Sum(nil)), len(parts)), nil
}

// ---------------------------------------------------------------------------
// Copy.
// ---------------------------------------------------------------------------

// POST /file/copy
func (s *Server) handleCopy(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Team     string `json:"team"`
		Game     string `json:"game"`
		PathFrom string `json:"pathFrom"`
		PathTo   string `json:"pathTo"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	obj, ok := s.state.CopyScratch(body.Team, body.Game, body.PathFrom, body.PathTo)
	if !ok {
		writeErr(w, http.StatusNotFound, "source object not found in scratch")
		return
	}
	writeJSON(w, http.StatusOK, api.CopyResponse{
		Key:  obj.Key,
		ETag: obj.ETag,
	})
}

// POST /file/multicopy
func (s *Server) handleMultiCopy(w http.ResponseWriter, r *http.Request) {
	var body api.FileMulticopyBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	// Just record the destination directly — the client doesn't do per-part
	// PUTs during a multicopy in the real API either; it's a server-side op.
	s.state.PutScratch(body.Team, body.Game, body.PathTo, body.ETag, body.Size)

	// Return an empty Parts list to signal "nothing more to do".
	writeJSON(w, http.StatusOK, api.MultiUploadInitResponse{
		Key:      body.Team + "/" + body.Game + body.PathTo,
		UploadID: "copy-" + randomToken(8),
		Parts:    []api.MultiUploadPartResponse{},
	})
}

// ---------------------------------------------------------------------------
// Delete.
// ---------------------------------------------------------------------------

// POST /file/delete
func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	var body api.DeleteBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	var deleted int
	var failed []string
	for _, p := range body.Paths {
		if s.state.DeleteScratch(body.Team, body.Game, p) {
			deleted++
		} else {
			failed = append(failed, p)
		}
	}
	writeJSON(w, http.StatusOK, api.DeleteResponse{
		Deleted:     deleted,
		FailedPaths: failed,
	})
}

// ---------------------------------------------------------------------------
// Publish.
// ---------------------------------------------------------------------------

// POST /file/publish/{kind}
func (s *Server) handlePublish(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	if kind != "math" && kind != "front" {
		writeErr(w, http.StatusBadRequest, "unknown publish kind: "+kind)
		return
	}
	var body api.PublishBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}

	// Small visual delay so the "Publishing..." line is noticeable in demo.
	s.sleep(s.publishDelay)

	// Scripted failure: if the game slug ends with "-fail", return an error
	// that the TUI renders in its publish failure path. Useful for
	// recording the "override warning → fail → investigate" story.
	if strings.HasSuffix(body.Game, "-fail") {
		writeJSON(w, http.StatusOK, api.PublishResult{
			Code:    "MATH_MISSING_MODE",
			Message: "missing weights file for mode 'bonus_buy'",
		})
		return
	}

	version := s.state.NextPublishVersion(body.Team, body.Game, kind)
	writeJSON(w, http.StatusOK, api.PublishResult{
		Version: version,
		Changed: true,
	})
}

// ---------------------------------------------------------------------------
// Control / reset.
// ---------------------------------------------------------------------------

// POST /__reset — wipes all mutable state. Handy between recording takes.
func (s *Server) handleReset(w http.ResponseWriter, _ *http.Request) {
	s.state.Reset()
	_, _ = w.Write([]byte("reset ok\n"))
}

// ---------------------------------------------------------------------------
// Response helpers.
// ---------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("writeJSON: %v", err)
	}
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
