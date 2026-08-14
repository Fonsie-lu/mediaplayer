# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
make build                              # go build -o mediaplayer .
make run                                # go run .
make test                               # go test -v ./...
make fmt                                # gofmt -w .
make clean                              # rm binary + /tmp/mediaplayer-* (session AND preview caches)
go test -v -run TestName ./...          # single test
./mediaplayer                           # default config ~/.config/mediaplayer.json (XDG_CONFIG_HOME honored)
./mediaplayer -config /path/config.json # alternate config
./mediaplayer -no-tui                   # headless even on a TTY
```

For manual verification of a streaming change, run headless against a scratch config: `./mediaplayer -config /tmp/scratch.json -no-tui`. Headless is the point — with the TUI up, `log` output is redirected into the in-memory ring buffer and you have to hunt it in the Logs tab; headless leaves ffmpeg's per-batch lines on stderr where a `tee`/`grep` can reach them. A scratch config keeps the throwaway mount out of `~/.config/mediaplayer.json`, which the TUI and `/api/config` rewrite in place.

First run with a config path that doesn't exist is not an error: `config.Load` writes the defaults (`0.0.0.0:8090`, no mounts, no disk) to that path and returns them, so the server comes up on an empty browser page and creates the file. Load also truncates the mounts slice to `MaxMounts` (10, the number of positional keybinds) and `filepath.Clean`s every mount path, so a trailing slash in the JSON is gone by the time any handler sees it.

Tests are pure unit tests (`sortEntries`/`classify`, `safeJoin`/`resolveMount`, `NumSegments`/`PlaylistText`/boundaries, `parseKeyframeCSV`/`BuildBoundaries`/`maxGap`, `pickPreferredAudio`, `mountpointFor`/`unescapeMountField` against a table literal rather than the real `/proc/mounts`, `sheetTimes`/`hhmmss`) — none shell out to ffmpeg or read host state, so `make test` runs anywhere, including non-Linux where `statfsUsage` is the stub. The streaming path (batching, session concurrency, ffmpeg arg construction) has **no** automated coverage; verify those changes by playing a real file.

Go floor is the `go` directive in `go.mod` (1.26.0); the repo is kept `gofmt -l`-clean, and gofmt's comment-alignment rules shift between releases, so run `make fmt` after editing rather than hand-aligning trailing comments. `min`/`max` are the language builtins — `internal/tui` used to carry int-only copies that shadowed them; don't reintroduce those.

## External dependencies (must be on PATH)

- `ffmpeg` — HLS transcoding
- `ffprobe` — codec/container detection for direct-vs-transcode decision
- `ffmpegthumbnailer` — 300px PNG previews
- `hls.js` — fetched from jsdelivr CDN in `web/player.html`. For offline / air-gapped use, vendor it into `web/vendor/` and update the `<script src>`. Safari has native HLS and doesn't need it.
- JetBrains Mono — `@import`ed from Google Fonts at the top of `web/css/tokyo-night.css`. Second CDN hit to remove for air-gapped use; the `--mono` stack already falls back to locally installed Nerd Fonts, so dropping the `@import` degrades gracefully.

## Architecture

Stdlib-only HTTP/streaming backend + vanilla JS frontend. `//go:embed all:web` bakes all static assets into the binary. The one third-party dependency is the optional terminal UI (Bubble Tea / Lip Gloss, in `internal/tui`); the server, API, transcode, and session layers remain stdlib-only.

### TUI control panel & startup modes

`main.go` decides at startup whether to run the TUI: when both stdin and stdout are TTYs and `-no-tui` is not set, the HTTP server runs in a background goroutine and `tui.Run` takes the foreground; otherwise it stays headless and blocks on `SIGINT`/`SIGTERM` exactly as before. In TUI mode the standard logger is redirected (`log.SetFlags(0)` + `log.SetOutput(applog.Default)`) into `internal/applog`, an in-memory ring buffer — writing to stderr would corrupt the rendered UI.

`internal/applog` parses each log line for a `[session <id>]` tag and remembers the filename from `opened path=… dur=` lines (session id → file map), so the Logs tab can group entries by `(session, filename)`. It exposes a `Version()` counter the TUI polls (700ms `tea.Tick`) to know when to rebuild.

`internal/tui` is a single Bubble Tea model (`Model`) with three tabs — Mounts, Stars, Logs — and vim navigation (`j`/`k`, `g`/`G`, `tab`/`shift+tab`/`H`/`L`/`[`/`]`/`1`·`2`·`3`). Mounts edits go through `config.Replace` (persisted + live); Stars uses the exported `StarStore.List`/`Remove`; Logs renders collapsible per-group rows with a per-group entry count (the "sum"). Destructive actions route through one shared confirm modal (`confirmKind`: delete mount, unstar, restart) that swallows all input while open, so `ctrl+r` prompts before it sets `Model.restart` and quits; `main` then runs the normal graceful shutdown (`CloseAll` + `srv.Shutdown`) before `reexec()` calls `syscall.Exec` on the same binary/args/env. Because the listener is closed on exec and the server was already shut down, the re-exec'd process rebinds the port cleanly.

The TUI is the _only_ consumer of `StarStore.List`/`Remove` and of `applog` — both exist for it, so don't inline them away when refactoring the API package.

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

#### Seek-landing correction (both modes)

Where `-ss` lands is container-dependent, and a batch whose first segment is missing its head leaves a hole at the seek position that no amount of refetching fills (hls.js backtracks to `n-1`, whose batch has the same defect, and stalls). Containers with a real keyframe index (matroska cues, mp4 `stss`) land at or _before_ the target; index-less ones (mpegts DVB recordings) binary-search to a byte offset and usable content only begins at the next keyframe — possibly seconds _after_ it. So `StartBatch` probes the landing first (`probeLanding` in remux mode — a 1-frame `-c copy` replay of the same seek; `keyframeLanding` in encode mode — a demux-only `ffprobe -read_intervals` scan), and when the landing overshoots, backs `seekAt` off and re-probes, up to 3 tries. Each mode then reconciles the early start differently, and this is where the two `-output_ts_offset` values come from:

- **Remux** keeps the early packets (copied data can't be trimmed): `anchor` = landed keyframe PTS, `-t` is extended by the landing slop, `-segment_times` are measured relative to `anchor`, and `tsOffset = seekAt` undoes the `-ss` rebase exactly. The batch's first segment simply starts a little early; PTS stay source-true so players align by timestamp.
- **Encode** decodes early then trims: `-vf trim=start=<backoff>,setpts=PTS-<backoff>/TB` (before any `scale`) cuts output back to exactly `StartSec` and re-bases the timebase, so `-t`, `-force_key_frames` and `tsOffset = StartSec` all see the same 0-at-`StartSec` timeline as the no-backoff path. A filter can't touch copied audio, so **any nonzero backoff forces an audio re-encode** even for otherwise-copyable aac/mp3 (`-af atrim,asetpts` mirrors the video trim).

Both probes are best-effort: on error the code proceeds with the uncorrected seek and logs a "first segment will be short" warning rather than failing the batch.

### Session lifecycle

`internal/session/manager.go` maps cookie `mp_sid` → active `Session`. One transcode per cookie; `Adopt()` stops the previous batch and `RemoveAll`s its temp dir before swapping in the new one. SIGINT/SIGTERM triggers `CloseAll()` (which also stops the reaper). The `/api/stream/direct` handler calls `Close(sid)` so opening a direct-playback video releases any prior transcode session. `StartReaper()` runs a 30s ticker that closes sessions idle > `IdleTimeout` (10 min) — safety net for browsers that close without firing `pagehide`/`/api/stream/close`. `CleanStaleTempDirs()` runs at startup to wipe leftovers from prior crashed runs, matching every prefix in `staleTempPrefixes`. The dirs are created in `internal/api` but named here, as exported consts (`session.SessTempPrefix`, `session.SheetTempPrefix`) that the `os.MkdirTemp` calls in `stream.go`/`sheet.go` use — the janitor is the reason the prefixes need names at all, and a literal in two packages would let a rename silently orphan a dir instead of failing to compile. Add a prefix there rather than a second cleanup pass elsewhere. (`api` imports `session`, not the reverse, so this direction is the only one available.)

### Caching layers

Four independent caches, none of them shared:

- `transcode.probeCache` — ffprobe results keyed `path|size|mtime`, max 512 entries, **flushed wholesale** when full (not LRU). Exists because the player page probes a file and then opens a stream within the same second.
- `transcode.keyframeCache` — same key scheme, max 128, same flush-all policy. The scan demuxes every packet header (seconds on large files over network mounts) for a tiny result.
- Preview thumbnails — a **stable** dir `$TMPDIR/mediaplayer-previews`, sha1(`path|mtime|size`) filenames, so they survive restarts and don't accumulate one dir per launch. Concurrent requests for the same thumbnail serialize on a per-cachePath mutex in a `sync.Map` (`thumbGen`) instead of spawning duplicate `ffmpegthumbnailer` runs. `CleanStaleTempDirs` deliberately does not touch this dir (its prefixes don't match); `make clean` does.
- Session segment dirs — `os.MkdirTemp("", "mediaplayer-sess-")`, per session, deleted on session shutdown.
- Thumbnail-sheet frames — **not** a cache: `mediaplayer-sheet-*` per request, `RemoveAll`ed before the response is written (see below).

### Stars

Server-side, not localStorage: `internal/api/stars.go` persists `[]StarRef{mount, path}` to `~/.config/mediaplayer-stars.json` (path chosen in `main.go::defaultStarsPath`, so `XDG_CONFIG_HOME` applies). `mount` is the mount **index as a string** (whatever the client sent), keyed `mount + ":" + path` — so renumbering mounts orphans stars. Every mutation rewrites the whole file and rolls the in-memory set back if the write fails, keeping memory and disk in sync. Endpoints: `GET /api/stars`, `POST /api/stars/toggle`. The browser page mirrors them into a `Set` at load and renders a `★` in the meta column; `y` toggles.

Note `.gitignore` still lists a root-level `stars.json` from before the move into `~/.config`.

### Path safety

`internal/api/paths.go::safeJoin` runs **two** checks, and both are load-bearing. Every handler that touches the filesystem goes through `Handler.target`/`queryTarget`, which wrap it.

1. **Lexical** — prepend `/`, `filepath.Clean`, join onto the mount root, then require the result to be the root or `root + separator`-prefixed (`within`). Prepending `/` first is what makes `../../../etc` collapse to `/etc` and land _inside_ the mount instead of climbing out.
2. **Symlink** — `resolveExisting` on both the root and the joined path, then the same `within` check. Clean can't see links; one stored in a mount may point anywhere on the host. `filepath.EvalSymlinks` errors on missing paths, so `resolveExisting` resolves the longest existing prefix and re-appends the rest verbatim — a component that doesn't exist can't be a link, and rename destinations legitimately don't exist yet.

Order matters and is deliberate: because `..` is collapsed _before_ any link is traversed, `escape/../out` (where `escape` → outside) names `root/out` rather than following the link out the way the OS would. Consequences worth knowing:

- Symlinks **inside** one mount work. Links leaving a mount are rejected — including into _another_ configured mount, and including deleting a stray outward link via `/api/delete` (it 400s instead).
- `safeJoin` returns the **unresolved** path. `del` compares it against `mount.Path` and `rename` rebuilds sibling paths from it; resolving would break both when a mount root is itself a symlink (which is supported and tested).
- A prefix that exists but can't be resolved (permissions) is left as-is rather than failing the request — the handler's own filesystem call rejects it anyway.

The check is resolve-then-open, so it is not TOCTOU-proof: anything able to create a symlink inside a mount between the check and the handler's `open` can still win the race. Only the app itself writes into mounts (`rename`), so this is accepted, not solved — don't describe `safeJoin` as airtight against a local attacker with write access to a mount.

`TestSafeJoinSymlinks` builds a real tree (inside-link, escaping dir link, escaping file link, symlinked mount root) — extend it rather than reasoning about this by hand.

### Frontend state

`web/js/browser.js` is a single IIFE with a flat `state` object. Cursor memory (per `{mountIdx, path}`) and sort preference persist in localStorage. All vim bindings live in one `keydown` handler; mounts 1-9/0 jump by index. Filter, sort, rename, delete share a single reusable modal. `web/js/api.js` is the only place that builds API URLs — add endpoints there, not inline in the pages.

`web/js/player.js` has the same shape — one IIFE, flat state — but its keyboard handling is the inverse of the browser page's: a single **capture**-phase listener on `window` (see below), not a bubble-phase one on the document, because the native media controls would otherwise eat or double-act on the keys.

The user-facing binding tables for both pages live in `README.md` ("Client key bindings"), and they are the list to check before claiming a key is free — the handlers themselves are two separate `switch`es on two pages and reading only one is how collisions get shipped.

Keyboard is the primary interface but not the only one: below 900px (`@media` in `browser.css`) the mounts column collapses into a tap-driven `.mobile-nav` whose open state persists under `mp.mobilenav.open`. Keep new actions reachable from both.

#### Player keyboard handling (non-obvious, verified in-browser)

Chromium's native media controls live in a **closed user-agent shadow root** that swallows `keydown` outright: once a click lands on the play button or the scrubber, the event never reaches the page — not the document, not even a capture-phase listener on `window`. This is why the shortcuts appeared dead "when focus is on player elements", and the old capture-phase workaround for `q`/`Esc` never actually fixed that case either.

No pointer event escapes that shadow root either, so `focusin` on the video is the only usable signal. The player bounces pointer-driven focus back to the document there, keeping `:focus-visible` focus (real Tab navigation) intact. Two details are load-bearing:

- The blur **must be deferred** (`setTimeout(…, 0)`). Called synchronously inside the `focusin` dispatch it is ignored — the browser is mid-focus-assignment and re-applies focus.
- Mouse use of the controls is unaffected: clicking and dragging the scrubber still seek, because those are driven by pointer capture, not focus.

Everything else goes through one `keydown` listener on `window` in the **capture** phase, which `stopImmediatePropagation`s the keys it owns. Without that, the native controls also act on arrows/space (double seeks) and a focused nav button gets re-clicked by `space`. A focused `<select>` keeps only the keys it needs to operate (arrows, space, Enter, Escape, Home/End, PageUp/Down) — everything else still reaches the player, which is what makes the shortcuts survive using the quality/audio dropdowns.

Fullscreen is requested on `#stage`, **not** the `<video>`: only the fullscreen element's subtree renders, so fullscreening the bare video would hide the OSD and the shortcut card. The OSD and help card therefore live inside `.stage` in the markup. `Esc` while fullscreen is deliberately left to the browser (it would otherwise both exit fullscreen and leave the page).

There's a browser-driven test harness pattern that verified all of the above (real Chromium via puppeteer-core against `/usr/bin/chromium`, real key/mouse events, focus parked on each element in turn). It's worth rebuilding for any further key-handling change — this behavior is not reasonable to verify by reading code.

Resume positions live in localStorage under `mp.resume` (`"<mount>:<path>" → {t, dur, ts}`, capped at 200 entries, cleared when the playhead passes 95%). The player page writes them (throttled `timeupdate`, plus close/pagehide) and seeks to the stored position on open — unless the page URL carries `?t=`, which wins (see the thumbnail sheet below). That write-on-pagehide is worth remembering when testing resume behavior: leaving a player page rewrites the entry from the live playhead, so seed `mp.resume` from the browser page, not from the player you're about to navigate away from. The browser page renders them as a dim `▍ NN%` marker in the file list's meta column. While an HLS session is open the player also refetches the playlist every 4 min as a keepalive so a paused video isn't reaped by the 10-min idle timer (every HLS request `Touch()`es the session).

#### Disk usage & `.part` stripping (browser page)

The header shows used space for **the filesystem the browsed directory lives on**: the page sends its current `mount`/`path` to `GET /api/disk`, which reports `{ok, mountpoint, total_bytes, avail_bytes, used_bytes, percent_used}` — so the number follows navigation across mounts on different disks, and mounts alone are enough (no `disk` key needed). `config.Disk` (`"disk"` in the config file, a device node or a directory) is the **fallback**, used when the request carries no location (before the mount list loads) or that location can't be measured. `used_bytes` is `total - avail` where `avail` is statfs's **unprivileged**-available (`Bavail`), so root-reserved blocks read as used and the number is slightly higher than `df`'s "Used" column — deliberate, the widget answers "how much can a normal user still write". Two things are load-bearing:

- **`statfs` on `/dev/…` is wrong, not just unhelpful** — it reports the filesystem holding the device _node_ (devtmpfs, so "16G free" for every device). `internal/api/disk.go` therefore resolves a `/dev/`-prefixed spec through `/proc/mounts` first (`mountpointFor`, matching both the configured path and its `EvalSymlinks` form for `/dev/mapper`/`by-uuid` entries, shortest mountpoint wins because bind mounts and btrfs subvolumes repeat a device). Octal escapes in the table (`\040`) are unescaped. `statfsUsage` is build-tagged `disk_linux.go` / `disk_other.go`, same split as `ctime_*`.
- **Every failure is silent by design** — unset config, unmounted device, non-Linux build, and fetch errors all end as `{"ok":false}` (HTTP 200) and the widget stays `hidden`; the spec is "hide it rather than show a wrong number". Because the page polls every 60s, `logDiskChange` logs only on transitions — otherwise an unmounted disk writes one identical line per minute into the TUI's Logs tab. The dynamic path is silent in the same way: an unresolvable mount index or a rejected path falls through to the configured disk rather than erroring, and a directory that statfs can't enter (root-only vfat) falls back to the mount root first, since that is the same filesystem in every case but a nested mount.
- **The dynamic lookup goes through `resolveMount` + `safeJoin`** like every other path-taking handler (`currentDirUsage`) — the query names a mount-relative path, so it can't be pointed at an arbitrary host directory to read that filesystem's size. `statfs` itself needs no mount table (it resolves a directory to its filesystem); `/proc/mounts` supplies only the tooltip's mountpoint name, via `mountpointOf` — **longest** containing mountpoint, the opposite of `mountpointFor`'s shortest-wins rule, because a subvolume nested inside another filesystem is the one that holds the path. It matches on the symlink-resolved path, since that's the filesystem statfs measured. `contains` is `within` with `/` special-cased: `within` appends a separator to the root, which `/` already ends with, so the root mountpoint would never match.

The bar and the label both encode the _used_ fraction (green / `warn` >80% / `crit` >90%), so they grow together as the disk fills. The label is the bare percentage — the bar and the color already say "used". The widget refreshes on every `loadDir` (a different directory may be a different filesystem; not awaited, the listing must not wait on statfs) and after a delete, since that's the action that changes the number.

`Backspace` in the file list strips a trailing `.part` from a `*.mp4.part` name (case-insensitive) via the normal `/api/rename`, with no confirm modal. It refuses when the stripped name already exists in the listing — `os.Rename` clobbers silently, and the collision case is exactly "the finished file is already there". Note that guard is client-side only: `/api/rename` itself still overwrites, so the rename **dialog** can clobber.

Two things about that key: it used to be `q`, which meant `q` did unrelated things on the two pages, and `Backspace` used to be a third alias for "go up one directory" alongside `h`/`←`. Both were changed together — `Backspace` no longer navigates up, and `q` is unbound on the browser page.

#### Thumbnail sheet (`p`)

`p` on a video renders one frame per 10 minutes of runtime and shows them in an overlay whose frames are seek targets. `GET /api/sheet` (`internal/api/sheet.go`) returns `{interval, truncated, shots:[{t, w, h, data}]}` where `data` is a `data:image/jpeg;base64,…` URI. The client labels the sheet from `interval` rather than restating the constant.

Storing nothing is a requirement, not an optimization, and it dictates the shape: `ffmpegthumbnailer` can only write to a **file** (its `-o -` stdout mode interleaves `Seeking to time: …` progress lines into the image bytes, producing a corrupt JPEG), so each request gets one `os.MkdirTemp("", session.SheetTempPrefix)` that is `RemoveAll`ed by defer before the response is written. Nothing goes through `preview.go`'s stable cache — but both paths shell out through the one `runThumbnailer` in `preview.go`, which is the only place that knows the tool's flags. Note `ensureThumb` deliberately passes `context.Background()`: it writes straight into the cache path, so killing it on client abort would leave a truncated PNG that every later request treats as a hit. The sheet passes `r.Context()` because its frames are per-request scratch. Other things worth knowing before editing this:

- **`-t` with a bare number is a _percentage_**, not seconds — absolute seeks must be `hh:mm:ss` (`hhmmss()`), which also rounds so ffmpegthumbnailer never sees `00:00:60`.
- Frames are the **midpoints** of each 10-minute block (`sheetTimes`), not the block starts: `t=0` is reliably a black frame or a logo, and the final block is usually short. So a 25-min file yields 5:00 / 15:00 / 22:30.
- `sheetMax` (60 frames ≈ 10h) caps the work; hitting it logs and sets `truncated` rather than silently trimming. `sheetWorkers` (4) bounds the concurrent ffmpegthumbnailer processes, and the whole request runs under `r.Context()` plus `sheetTimeout`.
- A frame that fails is **dropped**, not fatal (truncated recordings fail to seek near the tail); only an empty result 500s, and per-frame errors log once per request via `sync.Once` so an unreadable file can't write 60 lines.
- `w`/`h` come from `jpeg.DecodeConfig` server-side precisely so the client can size the grid before any image decodes.

Client side (`browser.js`): `layoutSheet()` picks the column count that maximizes tile size subject to everything fitting the 80%-of-viewport box, which is what makes "no scrolling" hold at any window size or frame count — CSS wrapping can't do that. Its geometry is single-sourced: `--sheet-pad`/`--sheet-border`/`--sheet-gap`/`--sheet-head-h`/`--sheet-fraction` are declared on `:root` in `browser.css`, the `.sheet-*` rules use them, and `sheetMetric()` `parseFloat`s them back off `:root` — so keep those values plain px/numbers, and don't reintroduce JS-side copies. Tiles never upscale past the frame's own 300px. `resize` is coalesced to one layout per animation frame.

The overlay takes its full-viewport geometry from the shared `.modal` rule in `tokyo-night.css` (`.modal, .sheet`) and overrides only backdrop and stacking; it is **not** routed through `modal()`, which is a form with OK/Cancel and focus handling the sheet doesn't want. Closing clears `innerHTML` because the frames are megabytes of data URIs.

Each frame is a **`<button class="sheet-shot" data-t="…">`**, not a div: clicking one opens `/player?mount=…&path=…&t=<seconds>` for the file the sheet was rendered from (`state.sheetEntry`, captured at open — the cursor is not a reliable stand-in by then). The interaction rules are narrower than the old dismiss-on-anything overlay, and each half is load-bearing:

- One `mousedown`/`touchstart` listener on the overlay (`onSheetPointer`) covers every press: on a frame it plays, on the backdrop it closes, on the card's own chrome it does neither — a click while reading the header shouldn't dismiss. `touchstart` is deliberately **not** `{passive: true}`, since both acting branches `preventDefault` to stop a ghost click landing behind the overlay.
- The `keydown` guard still sits at the very top of the handler, ahead of the `#modal` guard, but only `Escape` closes now. `Tab` moves between frames and wraps at both ends rather than tabbing off into the invisible file list; `Enter`/`Space` on a focused frame calls `playSheetShot` **directly** rather than falling through to the button's own click, because the overlay listens on `mousedown` and a synthesized click would be a second differently-wired path to the same navigation. Every other key is swallowed with `preventDefault` and does nothing — without that, `G` jumps the cursor behind the overlay.
- Opening parks focus on the first frame (`focus({preventScroll: true})`). Tab order otherwise starts at the page chrome behind the overlay, leaving the frames several tabs from a keyboard that just pressed `p`; `closeSheet` hands focus back via `setActiveCol`.

Immediately below the guard, `Escape` during a still-rendering sheet aborts the fetch — the handler wires `r.Context()` into its ffmpegthumbnailers, so that actually stops the work rather than just hiding it.

The `t` the frames pass is a **page** URL param read by `player.js` (`requestedStart`), and it outranks the stored resume position for the first `play()` only; later `play()` calls (quality / audio switches) resume from the live playhead. Do not confuse it with the legacy `t` on `/api/stream/open`, which the server ignores (see spec constraints) — this one never leaves the client. The status line says `from 15:00` for a `?t=` jump and `resumed …` otherwise, which is the quickest way to tell in a browser which path ran.

### Routing

- `GET /` → `web/browser.html`
- `GET /player` → `web/player.html` (distinct URL so browser back returns to the browser page — spec requirement)
- `GET /css/*`, `/js/*` → embedded static
- `/api/*` → handlers in `internal/api/`

`internal/api/handler.go::Register` is the canonical endpoint list — read it rather than trusting an enumeration here or in the README, both of which have lagged the code before. `main.go` owns only `/`, the `/player` rewrite, and the embedded file server.

## Non-obvious spec constraints

- Spec mandates two separate pages, **not** a SPA. Browser history back must return to the file browser — don't merge the routes.
- Per-directory cursor memory (selected index preserved when navigating in/out of folders) is spec, not polish. Stored in `localStorage` under `mp.cursor`.
- Mount keybinds: `1`–`9` index 0–8, `0` indexes 9 (tenth mount). The spec says "1–10", treat as positional.
- Icons are Nerd Font glyphs — need a Nerd Font installed on the client for them to render.
- Sort default is `ctime_desc` (created newest first), and folders always sort before files regardless of the key.
- `ctime` needs a `syscall.Stat_t`, so `internal/api/ctime_linux.go` / `ctime_other.go` are build-tagged; the non-Linux fallback returns mtime. Keep both in sync when touching `FileEntry`.
- `/api/stream/open` still accepts the legacy `t` (start seconds) param and `api.js` still sends it, but the server ignores it — with VOD playlists the client seeks via standard HLS instead of re-spawning at an offset. Don't reintroduce a dependency on it. The `t` on the `/player` **page** URL is a different, live parameter (thumbnail-sheet deep links); the names collide but nothing connects them.
- Mount edits are live: `/api/config` POST and the TUI both call `config.Replace`, which mutates the shared `*config.Config` under its own lock and persists. Handlers must read mounts through `Snapshot()`/`MountByIndex()`, never cache them. `Replace` only touches mounts, so `disk` survives it — but `Save()` marshals the whole struct, so any save writes a `"disk"` key into configs that lacked one.
- Keybind collisions are easy to miss because the two pages have separate handlers. `q` was one (close player on `/player`, strip `.part` on `/`) and was resolved by moving the strip to `Backspace`; `/` is now free of `q` entirely. The binding tables in `README.md` are the cross-page view — check them, not one handler, before claiming a key is free.
