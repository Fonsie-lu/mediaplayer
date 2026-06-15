package session

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"mediaplayer/internal/transcode"
)

// Tunables. Spec: 1 min of buffering is sufficient.
const (
	SegDuration = 4.0  // seconds per HLS segment
	BatchSize   = 16   // segments per ffmpeg batch (~64s ≈ 1 min ahead)
	WindowBack  = 3    // segments behind current request to keep cached
	WindowAhead = 20   // segments ahead of current batch start to keep cached
	IdleTimeout = 10 * time.Minute
	reapTick    = 30 * time.Second
)

var (
	ErrSegmentTimeout = errors.New("timed out waiting for segment")
	ErrSessionClosed  = errors.New("session closed")
)

// Session represents one client's transcode state. With VOD-style playlists,
// the session is mostly metadata: input path, total duration, current ffmpeg
// batch (if any), and a tmp dir of cached segments.
type Session struct {
	ID        string
	Input     string  // absolute source path
	Duration  float64 // seconds
	MaxHeight int
	AudioIdx  int // audio-stream-relative index; ffprobe picks English when present
	Dir       string

	// CopyVideo enables remux mode: segments are stream-copied and split on
	// source keyframes, so Boundaries (NumSegments+1 ascending times, first 0,
	// last Duration) replaces the uniform SegDuration grid. Boundaries nil =
	// uniform grid (encode mode).
	CopyVideo  bool
	CopyAudio  bool
	Boundaries []float64

	mu         sync.Mutex
	batch      *transcode.Batch
	closed     bool
	lastAccess time.Time

	// startMu serializes batch replacement (stop → evict → clear → spawn →
	// install). Without it, two concurrent EnsureSegment calls that both
	// decide "need a new batch" while mu is released around StartBatch can
	// end up with two live ffmpeg processes writing the same segment paths —
	// in remux mode (in-place writes) that corrupts segments and fools the
	// successor-exists completeness check.
	startMu sync.Mutex
}

// segStart returns the source time at which segment i begins. i may equal
// NumSegments(), in which case the video duration is returned.
func (s *Session) segStart(i int) float64 {
	if s.Boundaries != nil {
		return s.Boundaries[i]
	}
	if t := float64(i) * SegDuration; t < s.Duration {
		return t
	}
	return s.Duration
}

// Touch marks the session as recently used so the idle reaper skips it.
// Called for every HLS request (playlist included), which lets the player
// keep a paused session alive with periodic playlist fetches.
func (s *Session) Touch() {
	s.mu.Lock()
	s.lastAccess = time.Now()
	s.mu.Unlock()
}

// NumSegments is the total segment count for the VOD playlist.
func (s *Session) NumSegments() int {
	if s.Boundaries != nil {
		return len(s.Boundaries) - 1
	}
	if s.Duration <= 0 {
		return 0
	}
	n := int(s.Duration / SegDuration)
	if float64(n)*SegDuration < s.Duration {
		n++
	}
	return n
}

// PlaylistText renders a complete VOD m3u8 covering the entire video. The
// client immediately knows total duration and can scrub anywhere; segment
// requests trigger on-demand transcoding.
func (s *Session) PlaylistText() string {
	n := s.NumSegments()
	maxDur := 0.0
	for i := 0; i < n; i++ {
		if d := s.segStart(i+1) - s.segStart(i); d > maxDur {
			maxDur = d
		}
	}
	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	b.WriteString("#EXT-X-VERSION:3\n")
	b.WriteString(fmt.Sprintf("#EXT-X-TARGETDURATION:%d\n", int(maxDur)+1))
	b.WriteString("#EXT-X-MEDIA-SEQUENCE:0\n")
	b.WriteString("#EXT-X-PLAYLIST-TYPE:VOD\n")
	b.WriteString("#EXT-X-INDEPENDENT-SEGMENTS\n")
	for i := 0; i < n; i++ {
		b.WriteString(fmt.Sprintf("#EXTINF:%.3f,\n", s.segStart(i+1)-s.segStart(i)))
		b.WriteString(fmt.Sprintf("seg_%05d.ts\n", i))
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	return b.String()
}

// EnsureSegment returns the path to seg_<n>.ts, transcoding it (and the next
// ~BatchSize segments) on demand if not already cached. Blocks until the
// requested file is complete, the deadline passes, or ctx is cancelled.
//
// Players don't request segments strictly in order — hls.js backtracks to
// n-1 after seeks and may pipeline n+1 — so concurrent requests can replace
// the session's batch out from under each other (a request for n-1 stops a
// batch that just started at n). Each waiter therefore loops: when the
// batch it was watching goes away, it re-evaluates the session state
// instead of failing, because the replacement batch usually covers its
// segment too.
//
// ctx is the client request's context. A waiter whose client has gone away
// (hls.js aborts in-flight segment loads on seeks and timeouts) must never
// spawn a batch — it would stop the batch a live request is waiting on, and
// the two would kill each other's ffmpeg until the deadline.
func (s *Session) EnsureSegment(ctx context.Context, n int) (string, error) {
	if n < 0 || n >= s.NumSegments() {
		return "", fmt.Errorf("segment %d out of range", n)
	}
	deadline := time.Now().Add(30 * time.Second)

	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return "", ErrSessionClosed
		}
		s.lastAccess = time.Now()
		b := s.batch
		// Complete on disk? Mind that a sequential (remux) batch writes in
		// place, so a present file it is still producing may be truncated.
		if segmentComplete(s.Dir, n, b) {
			s.mu.Unlock()
			return segPath(s.Dir, n), nil
		}
		// Being produced (or about to be) by the active batch? Wait for it.
		if b != nil && b.Contains(n) {
			s.mu.Unlock()
			path, err := waitForSegment(ctx, s.Dir, n, b, time.Until(deadline))
			if err == nil {
				return path, nil
			}
			if errors.Is(err, ErrSegmentTimeout) || ctx.Err() != nil {
				return "", err
			}
			// The batch exited without the segment. If it is still the
			// session's current batch, ffmpeg genuinely failed — give up.
			// Otherwise another request replaced it; re-evaluate.
			s.mu.Lock()
			replaced := s.batch != b
			s.mu.Unlock()
			if !replaced {
				return "", err
			}
			continue
		}
		s.mu.Unlock()

		// Need a new batch: stop the current one (if any), evict cache
		// outside the new window, then spawn ffmpeg starting at n. startMu
		// makes the whole replacement atomic with respect to other spawners,
		// so the session never has two live ffmpegs writing the same dir.
		s.startMu.Lock()
		// The world may have changed while we waited for startMu — another
		// request may have installed a batch that covers n, or the session
		// may have been closed. Re-evaluate before touching anything.
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			s.startMu.Unlock()
			return "", ErrSessionClosed
		}
		b = s.batch
		if segmentComplete(s.Dir, n, b) || (b != nil && b.Contains(n)) {
			s.mu.Unlock()
			s.startMu.Unlock()
			continue
		}
		s.batch = nil
		s.mu.Unlock()
		if ctx.Err() != nil {
			// Client gone — don't spawn a batch nobody will consume.
			s.startMu.Unlock()
			return "", ctx.Err()
		}
		if b != nil {
			b.Stop()
		}

		s.evictOutsideWindow(n)

		count := BatchSize
		if n+count > s.NumSegments() {
			count = s.NumSegments() - n
		}
		if s.CopyVideo {
			// The segment muxer writes in place. Leftover files inside the
			// new range (from a stopped earlier batch) would defeat the
			// successor-exists completeness check, so clear them; the batch
			// regenerates them anyway.
			for i := n; i < n+count; i++ {
				_ = os.Remove(segPath(s.Dir, i))
			}
		}
		var splits []float64
		if s.Boundaries != nil {
			for i := n + 1; i < n+count; i++ {
				splits = append(splits, s.Boundaries[i])
			}
		}
		nb, err := transcode.StartBatch(transcode.BatchSpec{
			Input:      s.Input,
			Dir:        s.Dir,
			StartSeg:   n,
			Count:      count,
			StartSec:   s.segStart(n),
			DurSec:     s.segStart(n+count) - s.segStart(n),
			SegDur:     SegDuration,
			MaxHeight:  s.MaxHeight,
			AudioIdx:   s.AudioIdx,
			CopyVideo:  s.CopyVideo,
			CopyAudio:  s.CopyAudio,
			SplitTimes: splits,
		})
		if err != nil {
			s.startMu.Unlock()
			return "", err
		}
		s.mu.Lock()
		if s.closed {
			// Session shut down while we were spawning; don't leak ffmpeg
			// into a removed dir.
			s.mu.Unlock()
			s.startMu.Unlock()
			nb.Stop()
			return "", ErrSessionClosed
		}
		s.batch = nb
		s.lastAccess = time.Now()
		s.mu.Unlock()
		s.startMu.Unlock()
		// Loop around to the b.Contains(n) wait branch.
	}
}

func segPath(dir string, n int) string {
	return filepath.Join(dir, fmt.Sprintf("seg_%05d.ts", n))
}

// segmentComplete reports whether seg_<n>.ts exists and is fully written.
// Files from encode batches appear atomically (hls muxer temp_file rename),
// so presence means complete. A sequential (remux) batch writes in place:
// while it is alive and n is in its range, the file is only complete once
// its successor has been opened; once the batch has exited, every file it
// left behind is complete (an abnormal exit removes its truncated tail).
func segmentComplete(dir string, n int, b *transcode.Batch) bool {
	st, err := os.Stat(segPath(dir, n))
	if err != nil || st.Size() == 0 {
		return false
	}
	if b == nil || !b.Sequential || !b.Contains(n) || b.Finished() {
		return true
	}
	if b.Contains(n + 1) {
		// The batch clears its range before starting, so a successor file
		// can only have been written — sequentially, after closing n — by
		// this batch.
		_, err = os.Stat(segPath(dir, n+1))
		return err == nil
	}
	// n is the batch's last segment — only process exit guarantees a flush.
	return false
}

// evictOutsideWindow deletes cached .ts files outside [n-WindowBack, n+WindowAhead].
// Bounds tmpfs RAM usage to ~1 batch's worth of segments per session.
func (s *Session) evictOutsideWindow(n int) {
	lo := n - WindowBack
	hi := n + WindowAhead
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "seg_") || !strings.HasSuffix(name, ".ts") {
			continue
		}
		idxStr := strings.TrimSuffix(strings.TrimPrefix(name, "seg_"), ".ts")
		idx, err := strconv.Atoi(idxStr)
		if err != nil {
			continue
		}
		if idx < lo || idx > hi {
			_ = os.Remove(filepath.Join(s.Dir, name))
		}
	}
}

// waitForSegment polls for segment n to be complete. Aborts early if the
// generating batch exits without producing it (ffmpeg failed or was killed)
// or the client request is cancelled.
//
// Completion criteria depend on the muxer: encode batches (hls muxer,
// temp_file flag) rename segments into place atomically, so existing =
// complete. Remux batches (segment muxer, Sequential) write in place, so a
// segment is only complete once its successor file has been opened or
// ffmpeg has exited — until then it may still be growing.
func waitForSegment(ctx context.Context, dir string, n int, b *transcode.Batch, timeout time.Duration) (string, error) {
	path := segPath(dir, n)
	deadline := time.Now().Add(timeout)
	t := time.NewTicker(50 * time.Millisecond)
	defer t.Stop()
	batchExited := false
	for {
		if segmentComplete(dir, n, b) {
			return path, nil
		}
		if batchExited {
			// One more check after exit, then give up. After exit every
			// produced file is closed (an interrupted sequential batch
			// already dropped its truncated tail).
			time.Sleep(50 * time.Millisecond)
			if segmentComplete(dir, n, b) {
				return path, nil
			}
			return "", fmt.Errorf("ffmpeg exited without producing %s", filepath.Base(path))
		}
		select {
		case <-t.C:
		case <-b.Done():
			batchExited = true
		case <-ctx.Done():
			return "", ctx.Err()
		}
		if time.Now().After(deadline) {
			return "", ErrSegmentTimeout
		}
	}
}

// ---------- Manager ----------

type Manager struct {
	mu       sync.Mutex
	sessions map[string]*Session
	stop     chan struct{}
	wg       sync.WaitGroup
}

func NewManager() *Manager {
	return &Manager{
		sessions: map[string]*Session{},
		stop:     make(chan struct{}),
	}
}

// Adopt replaces the session under sid, fully cleaning up any previous one.
func (m *Manager) Adopt(sid string, s *Session) {
	s.lastAccess = time.Now()
	m.mu.Lock()
	prev := m.sessions[sid]
	m.sessions[sid] = s
	m.mu.Unlock()
	if prev != nil {
		prev.shutdown()
	}
}

func (m *Manager) Get(sid string) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[sid]
	return s, ok
}

func (m *Manager) Close(sid string) {
	m.mu.Lock()
	s, ok := m.sessions[sid]
	delete(m.sessions, sid)
	m.mu.Unlock()
	if ok && s != nil {
		s.shutdown()
	}
}

func (m *Manager) CloseAll() {
	close(m.stop)
	m.wg.Wait()
	m.mu.Lock()
	all := m.sessions
	m.sessions = map[string]*Session{}
	m.mu.Unlock()
	for _, s := range all {
		if s != nil {
			s.shutdown()
		}
	}
}

// StartReaper kills sessions whose last access was more than IdleTimeout ago.
// Safety net for browsers that close without firing /api/stream/close.
func (m *Manager) StartReaper() {
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		t := time.NewTicker(reapTick)
		defer t.Stop()
		for {
			select {
			case <-m.stop:
				return
			case <-t.C:
				m.reapIdle()
			}
		}
	}()
}

func (m *Manager) reapIdle() {
	cutoff := time.Now().Add(-IdleTimeout)
	var stale []*Session
	m.mu.Lock()
	for sid, s := range m.sessions {
		s.mu.Lock()
		idle := s.lastAccess.Before(cutoff)
		s.mu.Unlock()
		if idle {
			delete(m.sessions, sid)
			stale = append(stale, s)
		}
	}
	m.mu.Unlock()
	for _, s := range stale {
		log.Printf("[session %s] reaped idle (>%s) dir=%s", s.ID, IdleTimeout, s.Dir)
		s.shutdown()
	}
}

// shutdown stops any active ffmpeg batch and removes the session's tmp dir.
// Taking startMu first waits out any in-flight batch spawn, so no ffmpeg can
// be started into the dir after it is removed; the closed flag makes every
// later EnsureSegment call fail fast.
func (s *Session) shutdown() {
	s.startMu.Lock()
	s.mu.Lock()
	s.closed = true
	b := s.batch
	s.batch = nil
	s.mu.Unlock()
	s.startMu.Unlock()
	if b != nil {
		b.Stop()
	}
	if s.Dir != "" {
		if err := os.RemoveAll(s.Dir); err != nil {
			log.Printf("session cleanup: %v", err)
		}
	}
}

// CleanStaleTempDirs removes leftover mediaplayer-sess-* directories from
// previous runs (e.g. after a crash or kill -9 of the server itself).
// Call once at startup.
func CleanStaleTempDirs() {
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "mediaplayer-sess-") {
			continue
		}
		full := filepath.Join(os.TempDir(), e.Name())
		if err := os.RemoveAll(full); err != nil {
			log.Printf("stale cleanup: %v", err)
		} else {
			log.Printf("removed stale session dir: %s", full)
		}
	}
}
