package main

import (
	"crypto/rand"
	"encoding/hex"
	"sync"

	"github.com/mnemoo/cli/internal/api"
)

// State is the mutable part of the mock server. Everything is guarded by a
// single mutex because the demo runs at a few requests per second — no need
// for fine-grained locking.
type State struct {
	mu sync.Mutex

	// scratch[team/game/type] -> path -> ScratchObject
	// Path keys are stored without the leading slash (matches real API).
	scratch map[string]map[string]api.ScratchObject

	// multipart[uploadID] -> multipartSession
	multipart map[string]*multipartSession

	// presignedTokens[token] -> pending put target.
	// One-time use: after the PUT completes the token is consumed.
	presigned map[string]*presignedTarget

	// publishedVersions tracks the next version number to return for
	// /file/publish/{math,front} per (team, game, kind).
	publishedVersions map[string]int
}

type multipartSession struct {
	Team     string
	Game     string
	Path     string // e.g. "/math/weights.csv"
	Key      string // opaque server-side key
	Filename string
	Size     int64
	Parts    []api.MultiUploadPartResponse
}

type presignedTarget struct {
	Team      string
	Game      string
	Path      string // e.g. "/math/weights.csv"
	Key       string
	ExpectETag string // if set, the scratch entry will use this etag after successful PUT
	// Multipart context (nil for single-part).
	Multipart *multipartContext
}

type multipartContext struct {
	UploadID string
	Part     int
}

func NewState() *State {
	s := &State{
		scratch:           make(map[string]map[string]api.ScratchObject),
		multipart:         make(map[string]*multipartSession),
		presigned:         make(map[string]*presignedTarget),
		publishedVersions: make(map[string]int),
	}
	// Seed publish version counters from fixture version history so the
	// next publish returns lastVersion+1 (e.g. v8 if history shows v7).
	for key, versions := range gameVersions {
		for _, v := range versions {
			pk := key + "/" + v.Type // "team/game/math" or "team/game/front"
			if v.Version > s.publishedVersions[pk] {
				s.publishedVersions[pk] = v.Version
			}
		}
	}
	return s
}

// Reset clears all mutable state. Call before each recording take.
func (s *State) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scratch = make(map[string]map[string]api.ScratchObject)
	s.multipart = make(map[string]*multipartSession)
	s.presigned = make(map[string]*presignedTarget)
	s.publishedVersions = make(map[string]int)
}

func scratchKey(team, game, uploadType string) string {
	return team + "/" + game + "/" + uploadType
}

// ListScratch returns the current scratch contents for (team, game, type).
func (s *State) ListScratch(team, game, uploadType string) []api.ScratchObject {
	s.mu.Lock()
	defer s.mu.Unlock()
	bucket := s.scratch[scratchKey(team, game, uploadType)]
	out := make([]api.ScratchObject, 0, len(bucket))
	for _, obj := range bucket {
		out = append(out, obj)
	}
	return out
}

// PutScratch records an object in the scratch bucket. uploadType is derived
// from the path prefix ("/math/..." -> "math", "/front/..." -> "front").
func (s *State) PutScratch(team, game, path, etag string, size int64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	uploadType := uploadTypeFromPath(path)
	if uploadType == "" {
		return
	}
	key := scratchKey(team, game, uploadType)
	bucket, ok := s.scratch[key]
	if !ok {
		bucket = make(map[string]api.ScratchObject)
		s.scratch[key] = bucket
	}
	// ScratchObject.Key is the path WITHOUT the leading slash (matches real API)
	storeKey := stripLeading(path, '/')
	bucket[storeKey] = api.ScratchObject{
		Key:  storeKey,
		ETag: etag,
		Size: size,
	}
}

// CopyScratch copies an existing object to a new path within the same
// (team, game) scope. The source must be resolvable by its key (without
// leading slash); the destination uses the same etag and size.
func (s *State) CopyScratch(team, game, pathFrom, pathTo string) (api.ScratchObject, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	srcType := uploadTypeFromPath(pathFrom)
	dstType := uploadTypeFromPath(pathTo)
	if srcType == "" || dstType == "" {
		return api.ScratchObject{}, false
	}

	srcBucket := s.scratch[scratchKey(team, game, srcType)]
	src, ok := srcBucket[stripLeading(pathFrom, '/')]
	if !ok {
		return api.ScratchObject{}, false
	}

	dstBucket, ok := s.scratch[scratchKey(team, game, dstType)]
	if !ok {
		dstBucket = make(map[string]api.ScratchObject)
		s.scratch[scratchKey(team, game, dstType)] = dstBucket
	}
	dstKey := stripLeading(pathTo, '/')
	dst := api.ScratchObject{Key: dstKey, ETag: src.ETag, Size: src.Size}
	dstBucket[dstKey] = dst
	return dst, true
}

// DeleteScratch removes an object by path. Returns true on success.
func (s *State) DeleteScratch(team, game, path string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	uploadType := uploadTypeFromPath(path)
	if uploadType == "" {
		return false
	}
	bucket := s.scratch[scratchKey(team, game, uploadType)]
	key := stripLeading(path, '/')
	if _, ok := bucket[key]; !ok {
		return false
	}
	delete(bucket, key)
	return true
}

// ---------------------------------------------------------------------------
// Presigned URL token bookkeeping.
// ---------------------------------------------------------------------------

func (s *State) RegisterPresigned(target *presignedTarget) string {
	token := randomToken(24)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.presigned[token] = target
	return token
}

func (s *State) ConsumePresigned(token string) (*presignedTarget, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.presigned[token]
	if ok {
		delete(s.presigned, token)
	}
	return t, ok
}

// ---------------------------------------------------------------------------
// Multipart session bookkeeping.
// ---------------------------------------------------------------------------

func (s *State) RegisterMultipart(session *multipartSession) string {
	uploadID := "mp_" + randomToken(16)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.multipart[uploadID] = session
	return uploadID
}

func (s *State) GetMultipart(uploadID string) (*multipartSession, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.multipart[uploadID]
	return m, ok
}

func (s *State) CompleteMultipart(uploadID string) (*multipartSession, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.multipart[uploadID]
	if ok {
		delete(s.multipart, uploadID)
	}
	return m, ok
}

// ---------------------------------------------------------------------------
// Publish version counter.
// ---------------------------------------------------------------------------

func (s *State) NextPublishVersion(team, game, kind string) int {
	key := team + "/" + game + "/" + kind
	s.mu.Lock()
	defer s.mu.Unlock()
	s.publishedVersions[key]++
	return s.publishedVersions[key]
}

// ---------------------------------------------------------------------------
// Helpers.
// ---------------------------------------------------------------------------

func randomToken(n int) string {
	buf := make([]byte, n)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

func stripLeading(s string, c byte) string {
	if len(s) > 0 && s[0] == c {
		return s[1:]
	}
	return s
}

// uploadTypeFromPath extracts "math" or "front" from a remote path like
// "/math/base_weights.csv". Returns "" if the prefix doesn't match.
func uploadTypeFromPath(p string) string {
	p = stripLeading(p, '/')
	if len(p) >= 5 && p[:5] == "math/" {
		return "math"
	}
	if len(p) >= 6 && p[:6] == "front/" {
		return "front"
	}
	// Accept bare "math" / "front" (e.g. when scratch bucket is addressed directly).
	if p == "math" || p == "front" {
		return p
	}
	return ""
}
