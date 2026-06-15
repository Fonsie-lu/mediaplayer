# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
make build                              # go build -o mediaplayer .
make run                                # go run .
make test                               # go test -v ./...
go test -v -run TestName ./...          # single test
./mediaplayer -config /path/config.json # alternate config
```

## External dependencies (must be on PATH)

- `ffmpeg` — HLS transcoding
- `ffprobe` — codec/container detection for direct-vs-transcode decision
- `ffmpegthumbnailer` — 300px PNG previews
- `hls.js` — fetched from jsdelivr CDN in `web/player.html`. For offline / air-gapped use, vendor it into `web/vendor/` and update the `<script src>`. Safari has native HLS and doesn't need it.

## Architecture

Stdlib-only Go backend + vanilla JS frontend. `//go:embed all:web` bakes all static assets into the binary.

### Stream decision flow

`/api/probe` → ffprobe → returns `direct: bool` plus `audio_tracks` (per-track codec/language). The frontend (`web/js/player.js`) picks based on that flag, the quality selector, and the audio-track selector:

- **Direct** (h264/vp8/vp9/av1 + aac/opus/vorbis/mp3 in mp4/webm/matroska, default audio track): `GET /api/stream/direct` serves the raw file via `http.ServeFile` — browser handles Range requests and seeking natively. No ffmpeg involved.
- **Remux** (h264 video in a non-direct container, or non-default audio track, no quality cap): `/api/stream/open` decides this server-side. Video is stream-copied bit-identically into mpegts HLS segments; only incompatible audio (ac3/dts/…) is re-encoded (aac/mp3 are copied too, `acopy` in the log). Segment boundaries come from the source's real keyframe timestamps (`transcode.KeyframeTimes` → `BuildBoundaries`, cached like probes), stored as `Session.Boundaries`. Falls back to transcode when keyframes are unscannable or >30s apart (`remuxMaxSegment`).
- **Transcode** (everything else, or any quality ≠ source): `/api/stream/open` only registers session metadata (input path, ffprobe duration, target height, audio track); no ffmpeg yet.

In both remux and transcode the client immediately gets a synthetic VOD playlist at `/api/stream/hls/{sid}/playlist.m3u8` enumerating every segment for the entire video (variable `EXTINF` from boundaries in remux mode), so the full timeline is visible and seekable from the start. `/api/stream/open` accepts `audio=N` (audio-stream-relative index, defaults to the probed English/default preference) and reports `"mode": "remux"|"transcode"` in its response.

### On-demand HLS batches

Each `seg_NNNNN.ts` request goes through `Session.EnsureSegment(n)` (in `internal/session/manager.go`). If complete on disk (`segmentComplete` — presence alone isn't enough for in-place remux batches), served immediately. Otherwise the session stops any previous batch and spawns a fresh `transcode.Batch` (in `internal/transcode/hls.go`) starting at segment `n` and producing `BatchSize` (~1 min) of segments forward. The handler polls for `seg_NNNNN.ts` to be complete, then serves it. Tunables: `SegDuration=4s`, `BatchSize=16`, `WindowBack=3`, `WindowAhead=20`. Segments outside `[n-WindowBack, n+WindowAhead]` are evicted whenever a new batch starts — bounds tmpfs RAM to ~1 batch's worth per session.

Players do not request segments strictly in order (hls.js backtracks to `n-1` after seeks and pipelines `n+1`), so concurrent requests can replace the session's batch out from under each other. `EnsureSegment` is a retry loop: a waiter whose batch was stopped re-evaluates the session and latches onto the replacement batch (which usually covers its segment) instead of returning an error; only a timeout or a genuine ffmpeg failure (batch died while still being the session's current batch) surfaces to the client. Batch replacement (stop → evict → clear → spawn → install) is serialized by `Session.startMu`, so a session can never have two live ffmpegs writing the same dir (in-place remux writes would corrupt segments and fool the successor-exists completeness check). `EnsureSegment` takes the request's `context.Context`: a waiter whose client aborted (hls.js cancels in-flight segment loads on seeks) returns immediately and never spawns a batch, so dead requests can't kill the batch a live request is waiting on. `shutdown` takes `startMu` and sets `Session.closed`, so a mid-spawn waiter can't start ffmpeg into a removed dir.

ffmpeg invocation uses `-ss <segment start>` (input-side seek), `-output_ts_offset <segment start>` so PTS in independently-generated batches share one global timeline (no `EXT-X-DISCONTINUITY` needed). The two modes diverge in `transcode.StartBatch` (`BatchSpec`):

- **Encode batches** use the hls muxer with `-force_key_frames "expr:gte(t,n_forced*SegDuration)"` so every segment starts with an IDR. The `temp_file` HLS flag means a segment file only appears (atomic rename) when fully written, so the wait poll never serves a partial file.
- **Remux batches** (`CopyVideo`) use the _segment_ muxer with explicit `-segment_times`, because a copy-mode `-ss` lands on whatever keyframe the container's seek index points at (often before the target) and count-based splitting would shift every segment. The landing keyframe is detected first by `probeLanding` (a 1-frame `-c copy` replay of the same seek) since the muxer measures split times from the batch's first packet; the landing slop pads the batch's first segment, later splits land exactly on the playlist's keyframe boundaries. The segment muxer writes files in place (no temp_file), so `waitForSegment` treats a remux segment as complete only once its successor file exists or ffmpeg exited (`Batch.Sequential`), and an abnormally-exiting batch deletes its possibly-truncated newest file (`removeNewestSegment`).

Both modes set `-muxdelay 0` + `-avoid_negative_ts make_non_negative` so segment content starts at exactly its playlist time. Without this, mpegts adds a 1.4s start offset to every segment; after a seek the fetched fragment then doesn't cover the playhead and hls.js backtracks (`n, n-1, n+1` request storms that used to kill batches mid-flight).

No `-re`: bounded batch length is the rate limit.

### Session lifecycle

`internal/session/manager.go` maps cookie `mp_sid` → active `Session`. One transcode per cookie; `Adopt()` stops the previous batch and `RemoveAll`s its temp dir before swapping in the new one. SIGINT/SIGTERM triggers `CloseAll()` (which also stops the reaper). The `/api/stream/direct` handler calls `Close(sid)` so opening a direct-playback video releases any prior transcode session. `StartReaper()` runs a 30s ticker that closes sessions idle > `IdleTimeout` (10 min) — safety net for browsers that close without firing `pagehide`/`/api/stream/close`. `CleanStaleTempDirs()` runs at startup to wipe leftover `mediaplayer-sess-*` dirs from prior crashed runs.

### Path safety

`internal/api/paths.go::safeJoin` prepends `/` then `filepath.Clean`s the user-supplied rel path before joining, so `../../../etc` collapses to `/etc` and joins under the mount root rather than escaping. Every handler that touches the filesystem goes through it.

### Frontend state

`web/js/browser.js` is a single IIFE with a flat `state` object. Cursor memory (per `{mountIdx, path}`) and sort preference persist in localStorage. All vim bindings live in one `keydown` handler; mounts 1-9/0 jump by index. Filter, sort, rename, delete share a single reusable modal.

Resume positions live in localStorage under `mp.resume` (`"<mount>:<path>" → {t, dur, ts}`, capped at 200 entries, cleared when the playhead passes 95%). The player page writes them (throttled `timeupdate`, plus close/pagehide) and seeks to the stored position on open; the browser page renders them as a dim `▍ NN%` marker in the file list's meta column. While an HLS session is open the player also refetches the playlist every 4 min as a keepalive so a paused video isn't reaped by the 10-min idle timer (every HLS request `Touch()`es the session).

### Routing

- `GET /` → `web/browser.html`
- `GET /player` → `web/player.html` (distinct URL so browser back returns to the browser page — spec requirement)
- `GET /css/*`, `/js/*` → embedded static
- `/api/*` → handlers in `internal/api/`

## Non-obvious spec constraints

- Spec mandates two separate pages, **not** a SPA. Browser history back must return to the file browser — don't merge the routes.
- Per-directory cursor memory (selected index preserved when navigating in/out of folders) is spec, not polish. Stored in `localStorage` under `mp.cursor`.
- Mount keybinds: `1`–`9` index 0–8, `0` indexes 9 (tenth mount). The spec says "1–10", treat as positional.
- Icons are Nerd Font glyphs — need a Nerd Font installed on the client for them to render.
- Sort default is `ctime_desc` (created newest first).
