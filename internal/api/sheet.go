package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image/jpeg"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"mediaplayer/internal/session"
	"mediaplayer/internal/transcode"
)

const (
	sheetInterval = 600.0 // one frame per 10 minutes of runtime
	sheetSize     = 300   // ffmpegthumbnailer -s: px on the longer edge
	sheetMax      = 60    // ceiling on frames per request (10h of video)
	sheetWorkers  = 4
	sheetTimeout  = 3 * time.Minute
)

// sheetShot carries one frame inline. W/H come from the encoded JPEG so the
// browser can lay the grid out before a single image has loaded.
type sheetShot struct {
	T    float64 `json:"t"`
	W    int     `json:"w"`
	H    int     `json:"h"`
	Data string  `json:"data"` // data: URI — deliberately not cached anywhere
}

// sheet renders one thumbnail per 10 minutes of the target video and returns
// them inline. Unlike /api/preview nothing is cached: the frames live in a
// per-request temp dir that is removed before the response is written, so the
// only lasting copy is the one in the client's modal.
func (h *Handler) sheet(w http.ResponseWriter, r *http.Request) {
	_, full, ok := h.queryTarget(w, r)
	if !ok {
		return
	}
	if _, err := os.Stat(full); err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	probe, err := transcode.Probe(full)
	if err != nil {
		// ffprobe's "exit status 1" says nothing useful in a status bar; the
		// detail belongs in the log.
		log.Printf("sheet %s: probe failed: %v", filepath.Base(full), err)
		writeErr(w, http.StatusUnprocessableEntity, "cannot read this video")
		return
	}
	if probe.Duration <= 0 {
		writeErr(w, http.StatusUnprocessableEntity, "unknown duration")
		return
	}
	times := sheetTimes(probe.Duration)
	truncated := false
	if len(times) > sheetMax {
		log.Printf("sheet %s: %d frames capped to %d", filepath.Base(full), len(times), sheetMax)
		times, truncated = times[:sheetMax], true
	}

	ctx, cancel := context.WithTimeout(r.Context(), sheetTimeout)
	defer cancel()
	// ffmpegthumbnailer can only write to a file — its stdout mode interleaves
	// progress lines into the image data — so the frames make a brief stop on
	// disk in here.
	dir, err := os.MkdirTemp("", session.SheetTempPrefix)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer os.RemoveAll(dir)

	started := time.Now()
	shots := renderSheet(ctx, full, dir, times)
	if len(shots) == 0 {
		writeErr(w, http.StatusInternalServerError, "no frames could be rendered")
		return
	}
	log.Printf("sheet %s: %d/%d frames in %s", filepath.Base(full), len(shots), len(times),
		time.Since(started).Round(time.Millisecond))
	writeJSON(w, http.StatusOK, map[string]any{
		"interval":  sheetInterval, // the client labels the sheet from this
		"truncated": truncated,
		"shots":     shots,
	})
}

// sheetTimes returns the midpoint of every sheetInterval-long block of the
// video. Midpoints rather than block starts: t=0 is routinely a black frame or
// a distributor logo, and the last block is usually short.
func sheetTimes(dur float64) []float64 {
	var out []float64
	for start := 0.0; start < dur; start += sheetInterval {
		end := start + sheetInterval
		if end > dur {
			end = dur
		}
		out = append(out, (start+end)/2)
	}
	return out
}

// renderSheet generates all frames with a bounded worker pool and returns the
// ones that succeeded, in time order. A frame that fails (an unseekable tail,
// a truncated recording) is dropped rather than failing the whole request.
func renderSheet(ctx context.Context, input, dir string, times []float64) []sheetShot {
	out := make([]*sheetShot, len(times))
	sem := make(chan struct{}, sheetWorkers)
	var wg sync.WaitGroup
	var logOnce sync.Once
	for i, t := range times {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if ctx.Err() != nil {
				return
			}
			shot, err := renderShot(ctx, input, filepath.Join(dir, strconv.Itoa(i)+".jpg"), t)
			if err != nil {
				// One line per request, not per frame: a file the tool can't
				// seek at all would otherwise log 60 near-identical failures.
				logOnce.Do(func() { log.Printf("sheet %s: %v", filepath.Base(input), err) })
				return
			}
			out[i] = shot
		}()
	}
	wg.Wait()
	shots := make([]sheetShot, 0, len(times))
	for _, s := range out {
		if s != nil {
			shots = append(shots, *s)
		}
	}
	return shots
}

func renderShot(ctx context.Context, input, tmp string, t float64) (*sheetShot, error) {
	if err := runThumbnailer(ctx, input, tmp, "jpeg", sheetSize, hhmmss(t)); err != nil {
		return nil, fmt.Errorf("frame at %s failed: %w", hhmmss(t), err)
	}
	raw, err := os.ReadFile(tmp)
	if err != nil {
		return nil, err
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("frame at %s: %w", hhmmss(t), err)
	}
	return &sheetShot{
		T:    t,
		W:    cfg.Width,
		H:    cfg.Height,
		Data: "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(raw),
	}, nil
}

func hhmmss(sec float64) string {
	if sec < 0 {
		sec = 0
	}
	s := int(sec + 0.5)
	return fmt.Sprintf("%02d:%02d:%02d", s/3600, (s%3600)/60, s%60)
}
