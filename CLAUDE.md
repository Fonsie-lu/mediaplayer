# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

**Scope of this file:** what you cannot get from reading one file — the map, the cross-file couplings, and the facts that live in no source file at all. The detailed *why* for each subsystem is a doc comment on the code itself; the pointers here name the exact function to read, and that comment is the authority when the two disagree. `README.md` owns the user-facing surface (features, config reference, the full key-binding tables) — don't restate it here.

## Commands

```bash
make build                              # go build -o mediaplayer .
make run                                # go run .
make test                               # go test -v ./...
make check                              # the gate: fmt-check + vet + lint (staticcheck) + test -race
make lint                               # go tool staticcheck ./...  (pinned in go.mod's tool block)
make race                               # go test -race ./...
make e2e                                # browser-driven checks (needs chromium + node + ffmpeg)
make fmt                                # gofmt -w .
make clean                              # rm binary + /tmp/mediaplayer-* (session AND preview caches)
go test -v -run TestName ./...          # single test
./mediaplayer                           # default config ~/.config/mediaplayer.json (XDG_CONFIG_HOME honored)
./mediaplayer -config /path/config.json # alternate config
./mediaplayer -no-tui                   # headless even on a TTY
./android/build.sh                      # writes ./mediaplayer.apk (JDK + Android SDK, no Gradle)
```

`make check` is the gate to run before calling Go work done. There is no CI workflow in the repo, so nothing enforces it elsewhere. staticcheck is a **tool dependency** in `go.mod`, not a separate install — the `tool` directive and its indirect requirements are the only reason go.sum lists `honnef.co/go/tools`; none of it links into the binary. The repo is currently `gofmt`-, `vet`- and staticcheck-clean, so any output is something you added.

`make e2e` is separate on purpose: it needs chromium, node and ffmpeg, while `check` must run anywhere. Run it for any key-handling or frontend-structure change — see "Browser-driven tests".

For manual verification of a streaming change, run headless against a scratch config: `./mediaplayer -config /tmp/scratch.json -no-tui`. Headless is the point — with the TUI up, `log` output goes into the in-memory ring buffer and you have to hunt it in the Logs tab; headless leaves ffmpeg's per-batch lines on stderr where `tee`/`grep` can reach them. A scratch config keeps the throwaway mount out of `~/.config/mediaplayer.json`, which the TUI and `/api/config` rewrite in place. First run against a nonexistent config path is not an error: `config.Load` writes the defaults (`0.0.0.0:8090`, no mounts, no disk) and comes up on an empty browser page.

The built `mediaplayer` binary and `mediaplayer.apk` are **tracked in git** and there is no `.gitignore`, so `make build`/`make e2e` shows up as a diff. `make clean` removes the binary and `/tmp/mediaplayer-*` but not the apk (a release artifact, rebuilt only by `android/build.sh`).

Go floor is the `go` directive in `go.mod` (1.26.0). gofmt's comment-alignment rules shift between releases, so run `make fmt` after editing rather than hand-aligning trailing comments. `min`/`max` are the language builtins — `internal/tui` used to carry int-only copies that shadowed them; don't reintroduce those.

### Test coverage, and what has none

Every test is a pure unit test — nothing shells out to ffmpeg or reads host state (`mountpointFor` runs against a table literal, not the real `/proc/mounts`), so `make test` runs anywhere, including non-Linux where `statfsUsage` is the stub. Keep it that way; the `*_test.go` files are the inventory.

ffmpeg **argument construction** is covered only because `StartBatch` is split into `planBatch` (impure — probes the input) and `batchArgs(spec, plan)` (pure), letting `hls_test.go` pin invariants otherwise discoverable only by watching a video stall. Preserve that split when editing.

**Batching and session concurrency have no automated coverage** beyond `-race` on the tests that exist. Verify those by playing a real file.

## External dependencies (must be on PATH)

- `ffmpeg` — HLS remux/transcode · `ffprobe` — the direct-vs-transcode decision · `ffmpegthumbnailer` — 300px previews and sheet frames
- `hls.js` — jsdelivr CDN in `web/player.html`; vendor into `web/vendor/` for air-gapped use (Safari has native HLS and doesn't need it)
- JetBrains Mono — `@import`ed from Google Fonts atop `web/css/tokyo-night.css`; the second CDN hit to remove for air-gap. The `--mono` stack falls back to local Nerd Fonts, so dropping it degrades gracefully.
- JDK + Android SDK — **only** for `./android/build.sh`; nothing at server runtime needs them.

## Architecture

Stdlib-only HTTP/streaming backend + vanilla JS frontend, `//go:embed all:web` baking the assets into the binary. The one third-party dependency is the optional TUI (Bubble Tea / Lip Gloss in `internal/tui`); server, API, transcode and session layers stay stdlib-only.

| Package              | Role                                                                        |
| -------------------- | --------------------------------------------------------------------------- |
| `main.go`            | embed, routing (`/`, `/player` rewrite), TTY detection, signals, re-exec    |
| `internal/config`    | live mutable config behind a lock, persisted on change                      |
| `internal/api`       | HTTP handlers, path safety, browse/stream/preview/sheet/stars/disk          |
| `internal/session`   | cookie → session map, on-demand segment batching, idle reaper, temp janitor |
| `internal/transcode` | ffprobe, keyframe scan, HLS batch construction, the two LRU caches          |
| `internal/applog`    | in-memory parsed log sink, grouped by (session, filename), for the TUI      |
| `internal/tui`       | Bubble Tea control panel — Mounts / Stars / Logs                            |
| `web/`               | embedded frontend: ES module graph (browser page) + one IIFE (player)       |

Dependency direction worth remembering: **`api` imports `session`, never the reverse.** That is why the session temp-dir prefixes are exported consts in `session` (used by the `os.MkdirTemp` calls in `api`) — the startup janitor that needs them lives there.

### Startup modes & the TUI

`main.go` runs the TUI when stdin **and** stdout are TTYs and `-no-tui` is unset: the HTTP server moves to a background goroutine, `tui.Run` takes the foreground, and the standard logger is redirected into `applog` (writing to stderr would corrupt the render). Otherwise it stays headless and blocks on `SIGINT`/`SIGTERM`. `applog` parses each line for a `[session <id>]` tag and the filename from `opened path=…` lines so the Logs tab can group by (session, filename), and exposes a `Version()` counter the TUI polls on a 700ms `tea.Tick` to know when to rebuild.

`ctrl+r` in the TUI sets `Model.restart` and quits; `main` then does the normal graceful shutdown (`CloseAll` + `srv.Shutdown`) before `reexec()` calls `syscall.Exec` on the same binary/args/env. The listener is closed on exec, so the new process rebinds the port cleanly. Destructive TUI actions (delete mount, unstar, restart) all route through one shared confirm modal that swallows input while open.

The TUI is the **only** consumer of `StarStore.List`/`Remove` and of `applog` — both exist for it. Don't inline them away when refactoring `api`. Mount edits go through `config.Replace` (persisted **and** live).

### Stream decision flow

`/api/probe` → ffprobe → `direct: bool` plus `audio_tracks` (per-track codec/language). `web/js/player.js` picks using that flag, the quality selector and the audio-track selector; `/api/stream/open` makes the remux-vs-transcode call server-side.

- **Direct** (h264/vp8/vp9/av1 + aac/opus/vorbis/mp3 in mp4/webm/matroska, default audio track) — `/api/stream/direct` serves the raw file via `http.ServeFile`. Browser handles Range and seeking; no ffmpeg.
- **Remux** (h264 video in a non-direct container, or a non-default audio track, no quality cap) — video is stream-copied bit-identically into mpegts HLS segments; only incompatible audio (ac3/dts/…) is re-encoded (aac/mp3 are copied too, `acopy` in the log). Segment boundaries are the source's real keyframe timestamps (`transcode.KeyframeTimes` → `BuildBoundaries`, cached like probes) held in `Session.Boundaries`. Falls back to transcode when keyframes are unscannable or more than `remuxMaxSegment` (30s) apart.
- **Transcode** (everything else, or any quality ≠ source) — `/api/stream/open` registers session metadata only; no ffmpeg yet.

In both HLS modes the client immediately gets a synthetic **VOD** playlist at `/api/stream/hls/{sid}/playlist.m3u8` listing every segment of the whole video (variable `EXTINF` from boundaries in remux mode), so the full timeline is seekable from the start. `open` accepts `audio=N` (audio-stream-relative, defaulting to the probed English/default preference) and reports `"mode": "remux"|"transcode"`.

### On-demand HLS batches

Each `seg_NNNNN.ts` request goes through `Session.EnsureSegment(ctx, n)` (`internal/session/manager.go`): complete on disk → served; otherwise the session installs a fresh `transcode.Batch` starting at `n` and producing `BatchSize` segments forward, then polls. Tunables at the top of that file: `SegDuration=4s`, `BatchSize=16` (~1 min), `WindowBack=3`, `WindowAhead=20`, `IdleTimeout=10m`. Segments outside `[n-WindowBack, n+WindowAhead]` are evicted on every batch start, bounding tmpfs to ~1 batch per session. No `-re`: bounded batch length is the rate limit.

The **concurrency contract** is the part to understand before touching this, and it is written out on `EnsureSegment`, `startBatchAt`, `segmentComplete` and `waitForSegment`. In short: players don't request segments in order (hls.js backtracks to `n-1` after a seek and pipelines `n+1`), so waiters retry and latch onto replacement batches instead of erroring; `startMu` serializes stop → evict → clear → spawn → install so a session can never have two live ffmpegs in one dir; the request `ctx` means an aborted client never spawns a batch that would kill a live one's ffmpeg; and `shutdown` takes `startMu` so nothing starts ffmpeg into a removed dir.

The two modes use **different muxers, and therefore different completeness rules** — the single most confusing thing in the package:

- **Encode** — hls muxer, `-force_key_frames` so every segment opens on an IDR, `temp_file` flag so a segment appears atomically. Present = complete.
- **Remux** (`CopyVideo`) — *segment* muxer with explicit `-segment_times`, because a copy-mode `-ss` lands on whatever keyframe the container's index points at and count-based splitting would shift every segment. It writes **in place**, so a segment counts as complete only once its successor exists or ffmpeg exited (`Batch.Sequential`), and an abnormal exit deletes the possibly-truncated newest file.

Both set `-output_ts_offset` (so independently generated batches share one global timeline — no `EXT-X-DISCONTINUITY`), plus `-muxdelay 0` and `-avoid_negative_ts make_non_negative` so segment content starts at exactly its playlist time. Without those two, mpegts adds a ~1.4s offset per segment and the fetched fragment after a seek doesn't cover the playhead, producing the `n, n-1, n+1` request storms that used to kill batches mid-flight.

**Seek-landing correction** is the other subtle piece: where `-ss` lands is container-dependent, and a first segment missing its head leaves a hole no refetch can fill. `planBatch` probes the landing, backs the seek off, and each mode reconciles the early start differently — remux keeps the early packets and measures splits from the landed keyframe, encode decodes early and trims back with `trim,setpts` (which is why **any nonzero backoff forces an audio re-encode**, since a filter can't touch copied audio). The full reasoning, including the two different `-output_ts_offset` values, is on `planBatch` and `batchArgs` in `internal/transcode/hls.go`; `hls_test.go` pins the resulting command lines.

### Session lifecycle

Cookie `mp_sid` → one active `Session` (`internal/session/manager.go`). `Adopt()` stops the previous batch and removes its temp dir; `/api/stream/direct` calls `Close(sid)` so direct playback releases any prior transcode; `StartReaper()` closes sessions idle past `IdleTimeout` for browsers that vanish without firing `pagehide`/`/api/stream/close`; `CloseAll()` on SIGINT/SIGTERM. `CleanStaleTempDirs()` at startup wipes leftovers matching `staleTempPrefixes` — add a prefix to that list rather than writing a second cleanup pass elsewhere.

### Caches

- `transcode.probeCache` (512) and `keyframeCache` (128) — the same `lru[V]` in `internal/transcode/cache.go`, keyed by `statKey` (`path|size|mtime`, empty and therefore uncacheable when the file can't be stat'ed). Probes are cached because the player page probes a file and opens a stream in the same second; keyframe scans because they demux every packet header (seconds over a network mount) for a tiny result. Eviction is one entry at a time — these were hand-rolled maps that flushed wholesale, which re-ran ffprobe on files that had just been warm.
- **Preview thumbnails** — a *stable* `$TMPDIR/mediaplayer-previews` dir, sha1(`path|mtime|size`) names, so they survive restarts. Same-path requests serialize on a per-cachePath mutex in a `sync.Map` (`thumbGen`) instead of spawning duplicate `ffmpegthumbnailer` runs. `CleanStaleTempDirs` deliberately spares it; `make clean` does not.
- **Session segment dirs** — per session, removed on shutdown.
- **Sheet frames** — not a cache at all: a per-request dir, `RemoveAll`ed before the response is written.

### Path safety

`internal/api/paths.go::safeJoin` runs a lexical check *then* a symlink check, and both are load-bearing; the order (collapse `..` before traversing links) is deliberate. Its doc comment and `resolveExisting`'s explain why. Every filesystem handler goes through `Handler.target`/`queryTarget`, which wrap it — new handlers must too.

Consequences to know without re-deriving them:

- Symlinks **inside** a mount work; links leaving one are rejected, including into another configured mount, and including deleting a stray outward link via `/api/delete` (400, not a delete).
- `safeJoin` returns the **unresolved** path on purpose: `del` compares it against `mount.Path` and `rename` rebuilds siblings from it, both of which break for a symlinked mount root if you resolve it.
- It is resolve-then-open, so **not TOCTOU-proof**. Only the app writes into mounts, so this is accepted rather than solved — don't describe `safeJoin` as airtight against a local attacker with write access to a mount.
- `TestSafeJoinSymlinks` builds a real tree (inside link, escaping dir link, escaping file link, symlinked root). Extend it instead of reasoning by hand.

### Stars

Server-side, not localStorage: `internal/api/stars.go` persists `[]StarRef{mount, path}` to `~/.config/mediaplayer-stars.json` (path from `main.go::configPath`, so `XDG_CONFIG_HOME` applies). The stored `mount` is the mount **name** (`mountIdentity`), because indices renumber on reorder/delete and used to silently re-point stars; the browser page still speaks indices, so the HTTP layer translates both ways and `GET /api/stars` returns an empty `mount` for an orphan so it stays listed and removable. Legacy index-keyed files migrate on load. `StarRef`, `migrateMount` and `save` carry the details, including why `save` needs `MkdirAll`. Endpoints: `GET /api/stars`, `POST /api/stars/toggle`; the page mirrors them into a `Set` and renders `★` in the meta column (`y` toggles).

### Frontend

The browser page is an **ES module graph** (`<script type="module">`): `dom.js` (shared mutable `state`, resolved `el` handles), `listing.js` (mounts, directory, cursor, preview, stars), `sheet.js`, `dialogs.js` (modal + filter bar), `disk.js`, and `browser.js` as the entry point holding the single `keydown` handler, the DOM listeners and start-up. **The import direction is one-way — dom ← disk ← listing ← {sheet, dialogs} ← browser** — which is why `clearFilter` sits in `dom.js` instead of next to the rest of the filter UI: putting it with `openFilter` would make `listing.js` and `dialogs.js` import each other. `api.js` stays a **classic** script defining the `window.api`/`fmtSize`/`fmtDate`/`fmtTime` globals both pages share; the player page has no module tree. Module semantics are load-bearing twice: deferred (so `el`'s `getElementById` calls need no DOMContentLoaded wrapper) and strict-mode.

Every rebuilt container's clicks are **delegated** — one `click` listener each for the file list, the mount list and the crumbs, all in `browser.js`, reading `data-i`/`data-path` off the element — because the markup is regenerated whenever the listing changes and per-row listeners meant re-attaching them every time. Render functions emit inert markup; keep the index in the markup rather than closing over it, and wire any new rebuilt list the same way.

The preview column has two modes: a video's thumbnail plus meta rows, or — cursor on a directory — that directory's own listing (icon and filename only, with a child tally as a footer under the rows), fetched with `state.sort` so it always matches the file list's active order. Child rows must not reuse the `meta` class: `.preview .meta` is a descendant selector carrying `width: 100%`, which collapsed the filename to zero width the first time around — the e2e suite now asserts the rendered name's width for exactly that reason. Those fetches are cached per `mount:path:sort` in `listing.js` and the cache is **cleared by `loadDir`**, which is also every mutation (rename, delete, sort change), so a cached child list can never outlive the listing it was read from. The cache is not a nicety: the cursor crosses folders faster than the fetches return, and both it and the `state.previewReq` guard (shared with the image preview) are what keep a held `j` from re-requesting and from painting a stale directory over a newer cursor position.

State in `localStorage`: `mp.cursor` (per `{mountIdx, path}`), `mp.sort`, `mp.resume`, and `mp.mobilenav.open` in `sessionStorage`. Resume entries are `"<mount>:<path>" → {t, dur, ts}`, capped at 200, cleared past 95%; the player writes them on throttled `timeupdate` and on close/pagehide, and the browser page shows them as a dim `▍ NN%` marker. The map is read inside `renderFiles`, i.e. once per rebuild (directory load, filter, sort, star) — cursor keys don't rebuild, so a back-navigation served from the **bfcache** would otherwise show markers from before the video was watched; `browser.js` has a `pageshow` handler that rebuilds when `ev.persisted`. Testing gotcha: **leaving a player page rewrites the entry from the live playhead**, so seed `mp.resume` from the browser page, not from the player you're about to navigate away from. A `?t=` on the `/player` URL outranks the stored position for the first `play()` only. While an HLS session is open the player refetches the playlist every 4 min as a keepalive, since every HLS request `Touch()`es the session against the 10-min reaper.

Keyboard is the primary interface but not the only one: below 900px (`@media` in `browser.css`) the mounts column collapses into a tap-driven `.mobile-nav`. Keep new actions reachable from both. **The user-facing binding tables in `README.md` are the cross-page view** — the two pages have separate `switch`es, and reading only one is how collisions get shipped.

Two app-wide keyboard rules:

- Both handlers bail on `ev.ctrlKey || ev.metaKey || ev.altKey` first, so browser shortcuts survive. This matters most for the sheet, which `preventDefault`s every key it doesn't own.
- **`q` dismisses a read-only full-viewport overlay** (the thumbnail sheet, the player's help card); anything with a text input — the `#modal` form, the filter bar — takes `Escape` only, because `q` there is typed text. Extend that rule rather than restating it.

`web/js/player.js`'s keyboard handling is the **inverse** of the browser page's: one **capture**-phase listener on `window`, not a bubble-phase one on the document. Chromium's native media controls live in a closed user-agent shadow root that swallows `keydown` outright once focus lands inside it, and no pointer event escapes either, so `focusin` is the only usable signal — the player bounces pointer-driven focus back to the document while leaving real Tab focus alone, and **the blur must be deferred a tick** or the browser re-applies focus. All of that is commented at the `keyHandler`/`focusin` wiring near the bottom of `player.js` and was verified in-browser, not read out of a spec. Fullscreen is requested on `#stage`, **not** the `<video>`: only the fullscreen element's subtree renders, so the bare video would hide the OSD and shortcut card (and see the Android note for a second reason). `Esc` while fullscreen is left to the browser.

#### Thumbnail sheet (`p`)

`GET /api/sheet` (`internal/api/sheet.go`) returns `{interval, truncated, shots:[{t, w, h, data}]}` with each frame as a `data:` URI, and the client labels the sheet from `interval` rather than restating the constant. **Storing nothing is a requirement, not an optimization**, and it dictates the shape: `ffmpegthumbnailer` can only write to a file (its stdout mode interleaves progress lines into the JPEG), so each request gets a temp dir that is `RemoveAll`ed before the response is written. Both this and the cached previews shell out through the one `runThumbnailer` in `preview.go` — the only place that knows the tool's flags. Note the deliberate context split there: `ensureThumb` passes `context.Background()` (a client abort would leave a truncated PNG that later requests treat as a cache hit), the sheet passes `r.Context()` (per-request scratch, so aborting really stops the work).

Traps, all commented in `sheet.go`: **`-t` with a bare number is a percentage**, so absolute seeks must be `hh:mm:ss` via `hhmmss()`; frames are block **midpoints** (`sheetTimes`), since `t=0` is reliably black or a logo; `sheetMax`(60)/`sheetWorkers`(4)/`sheetTimeout` bound the work and a failed frame is dropped rather than fatal; `w`/`h` come from `jpeg.DecodeConfig` server-side so the client can size the grid before any image decodes.

Client side is `web/js/sheet.js` (only the `keydown` guard stays in `browser.js`). `layoutSheet()` picks the column count maximizing tile size subject to fitting the 80%-of-viewport box — that is what keeps the sheet scroll-free at any window size or frame count, which CSS wrapping cannot do. Its geometry is **single-sourced in CSS**: `--sheet-pad`/`--sheet-border`/`--sheet-gap`/`--sheet-head-h`/`--sheet-fraction` on `:root` in `browser.css`, read back by `sheetMetric()`. Keep those values plain px/numbers and don't reintroduce JS-side copies. The overlay borrows `.modal`'s full-viewport geometry from `tokyo-night.css` but is **not** routed through `modal()` (a form with OK/Cancel it doesn't want), and `closeSheet` clears `innerHTML` because the frames are megabytes of data URIs. Frames are `<button>`s so keyboard works; `Enter`/`Space` calls `playSheetShot` directly rather than synthesizing a click, because the overlay listens on `mousedown`.

#### Disk usage widget

The header reports the filesystem **the browsed directory lives on**: the page sends its current `mount`/`path` to `GET /api/disk`, so the number follows navigation across mounts on different disks and needs no configuration. `config.Disk` (a device node or a directory) is only the **fallback**, for before the mount list loads or when the location can't be measured. `used_bytes` is `total - Bavail` — unprivileged-available, so root-reserved blocks read as used and the number sits slightly above `df`'s "Used" column, deliberately answering "how much can a normal user still write". Bar and label both encode the used fraction (`warn` >80%, `crit` >90%); the widget refreshes on every `loadDir` (not awaited — the listing must not wait on statfs) and after a delete.

Three things in `internal/api/disk.go` not to re-derive (its comments have the reasoning): **`statfs` on `/dev/…` is wrong**, not merely unhelpful — it measures devtmpfs, where every device shows "16G free" — so a `/dev/` spec goes through `/proc/mounts` first; the dynamic lookup goes through `resolveMount` + `safeJoin` like every other path-taking handler, so it can't be aimed at an arbitrary host directory; and **every failure is silent by design**, ending as `{"ok":false}` with HTTP 200 and a hidden widget, because the spec is "hide it rather than show a wrong number" (which is also why `logDiskChange` logs only on transitions — a polled widget would otherwise write one identical line per minute into the Logs tab). `mountpointFor` (shortest match wins) and `mountpointOf` (longest wins) look contradictory and aren't. `statfsUsage` is build-tagged `disk_linux.go`/`disk_other.go`, same split as `ctime_*`.

#### Browser-driven tests (`make e2e`)

`test/e2e/harness.mjs` exists because none of the keyboard or module-graph behavior above is verifiable by reading code: real Chromium (puppeteer-core against `/usr/bin/chromium`, override with `CHROME=`), real key and mouse events, focus parked on each element in turn. 44 checks at present — module graph loading cleanly, list navigation, delegated mouse clicks on a mount row / folder row / file row, the sheet's open/close/Tab-wrap/key-swallowing, `q` inert in the list and in a text-input dialog, filter, server-side stars surviving a reload, the disk widget resolving, the folder preview listing a directory's children in the same order as that directory's own listing, and on the player page the pointer-focus bounce plus an arrow-key seek.

Load-bearing details:

- It sets **`XDG_CONFIG_HOME`** into its temp fixture dir, not just `-config`: the stars file's path derives from the config dir with no flag of its own, so without this the harness toggles stars in your real `~/.config/mediaplayer-stars.json` — and, starring being a toggle, leaks state between runs. That is exactly how a passing star check turned out to be a leftover from the previous run.
- The pointer-focus check must be a real **click**: `:focus-visible` is the signal `player.js` keys off, and a programmatic `.focus()` right after a keystroke still counts as keyboard-driven, so it would test the branch that deliberately *keeps* focus.
- Console/network errors fail the run, but only for **our own origin** — hls.js and JetBrains Mono are CDN fetches, and an offline machine must not read as a page defect. Aborted media requests are ignored too.

It has already earned its keep: it caught `StarStore.save` writing without `MkdirAll`, which 500'd every star toggle on a machine whose config dir didn't exist yet.

### Android app (`android/`) — a second consumer of the same pages

`mediaplayer.apk` is a single-activity WebView wrapper (`android/src/ch/bithawk/mediaplayer/MainActivity.java`, no third-party deps) pointed at a user-entered server URL. There is no second client: it loads the very same `/` and `/player`, so **frontend changes ship to the phone with the server** and nothing in `android/` needs touching for a `web/` edit. `android/build.sh` drives aapt2/javac/d8/zipalign/apksigner directly (no Gradle, no network); the self-signed key at `~/.config/mediaplayer-android.jks` is unrecoverable — lose it and the app can't be updated in place. `android/README.md` has the detail.

The wrapper leans on four page behaviors, and breaking any of them breaks **only the phone, silently**, since desktop Chromium supplies all four itself: element fullscreen **on `#stage`** (the activity's `onShowCustomView` fires from it — a second, independent reason not to fullscreen the bare `<video>`); **real media events** on `document`, which the injected `MPHost` keep-awake script forwards to `FLAG_KEEP_SCREEN_ON`, so a playback path that never fires `play`/`pause`/`ended` lets the screen dim mid-film; **localStorage + the `mp_sid` cookie**, which everything stateful rides on; and **autoplay without a gesture**, without which HLS stalls waiting for a tap that already happened on the file list. `MainActivity.java` comments each of these on its own side.

Consequences: the sub-900px `.mobile-nav` layout is what the app *always* renders, so it isn't an edge case there; Nerd Font glyphs are tofu on Android (accepted, not a bug); `usesCleartextTraffic="true"` + `minSdk 26` are deliberate for a plain-HTTP LAN server.

### Routing

`GET /` → `web/browser.html`; `GET /player` → `web/player.html` (a distinct URL so browser back returns to the browser — spec); `/css/*`, `/js/*` → embedded static; `/api/*` → `internal/api`. **`internal/api/handler.go::Register` is the canonical endpoint list** — read it rather than trusting an enumeration here or in the README, both of which have lagged the code before. `main.go` owns only `/`, the `/player` rewrite and the embedded file server.

Routes carry their **method** in the pattern (`"POST /api/rename"`), so the mux answers a wrong verb with 405 + `Allow` and no handler inspects `r.Method` — don't add such a check back.

That only holds while **nothing registers a catch-all**: `http.ServeMux` synthesises 405 only when no pattern matched at all, so a bare `"/"` for the embedded file server would match `/api/rename` and turn every wrong-verb request into the file server's 404 — enforcement silently gone, one file away from the patterns that declare it. `main.go` therefore registers the pages and assets under exact patterns (`GET /{$}`, `GET /player`, `GET /css/`, `GET /js/`) and builds the whole surface in `newMux`, which exists so `main_test.go` can assert on the same routing table the server runs. `TestAPIMethodsNotShadowedByStaticRoutes` is that assertion; `internal/api`'s own tests cannot see this, since they register only the API's half.

Two further consequences: a `GET` pattern also matches `HEAD`, which is what `http.ServeFile` needs on the file-serving routes; and the wrong-method response is Go's plain-text 405, not the API's JSON error envelope. `HEAD` matching `GET` is free on the read-only routes but not on `/api/stream/open`, which adopts a session as a side effect — moving that route to POST is the tidier fix and would break any client running a cached copy of the old `api.js`. The HLS route is a **wildcard** pattern (`GET /api/stream/hls/{sid}/{file}`), so `r.PathValue` replaces the hand-rolled prefix-trim and split. `{file}` cannot span a slash and the mux resolves dot segments before matching, which is why the handler sanitises nothing: `file` either equals the playlist name or goes through `ParseSegName`, and the segment path is rebuilt from the parsed int. `handler_test.go` pins both — the verb matrix and the three ways the HLS route can fail.

## Shared helpers (use these rather than re-inlining)

Small things that exist once on purpose, because the alternative is the same code in three packages drifting apart:

- `transcode.SegPattern` / `SegName(n)` / `ParseSegName(name)` — the `seg_%05d.ts` format. ffmpeg's output pattern, `session.segPath`, `PlaylistText`, the eviction scan and the `/api/stream/hls/` URL parser all go through these, so a rename fails to compile instead of silently producing segments nobody looks for. `ParseSegName` rejects negatives, which keeps a hand-typed `seg_-1.ts` a 400 rather than a range error.
- `transcode.correctSeek(want, probe)` — the seek back-off walk both modes share; only the probe differs (`probeLanding` remux, `keyframeLanding` encode). `landingSlop`/`maxSeekTries` are its tunables.
- `api.writeJSON` / `writeErr` / `writeOK` / `decodeBody` — the HTTP envelope. `decodeBody` is the JSON-decode preamble the mutating handlers share; it writes its own error response, so callers just `return` on false. It checks no method — that is the route pattern's job now (see Routing).
- `api.Handler.target` / `queryTarget` — mount + path resolution with the safety checks. Every filesystem handler goes through one of them.
- `api.runThumbnailer` — the only place that knows ffmpegthumbnailer's flags.
- `config.normalizeMounts` — the `MaxMounts` truncation + `filepath.Clean` that both `Load` and `Replace` apply, so no handler ever Cleans a mount path itself.
- `tui.listNav(key, sel, n)` — the `j`/`k`/`g`/`G` keys all three tabs share, returning `ok` for "this was navigation". Tab-specific keys (logs' `ctrl+d`/`ctrl+u` paging) stay in that tab's switch.
- `listing.setFocus(i)` vs `listing.refreshList(i)` — the module's only two entry points for the file cursor, and picking the wrong one is the easy mistake. `setFocus` is a **cursor move**: clamp, persist, repaint just the two rows whose `data-focus` changed, update the preview — and it returns early when the index doesn't change, which is what stops a held `j` at the end of the list from re-requesting the same preview (a server-side `ffmpegthumbnailer` spawn) 30 times a second. `refreshList` is for when **the rows themselves changed** (filter, star, fresh directory): full rebuild, then land the cursor, without persisting — the listing it lands in may not be the one the remembered position belongs to. `j`/`k`, `g`/`G` and `focusByName` take the first; the filter bar, `toggleStar` and `loadDir` take the second. Using `setFocus` after mutating `state.filtered` leaves the old rows on screen. `renderFiles`/`renderFocus`/`clampFocus`/`updatePreview` are deliberately **not** exported, so those two are the whole contract.
- `dom.store(key, value, storage)` / `dom.loadJSON(key, storage)` — guarded web-storage access, defaulting to `localStorage` and taking `sessionStorage` for `mp.mobilenav.open`. Both swallow the private-mode/quota/corrupt-value throw; `loadJSON` matters most for `mp.cursor`, which is parsed during module evaluation, where an uncaught throw would take the whole page's module graph down.
- `web/js/api.js` — `json()` wraps fetch + error unwrapping, `post(url, body)` is the shared JSON-body shape the three mutating callers use, and this is where API URLs are built. Add endpoints here, not inline in the pages. One exception: `player.js`'s `navigator.sendBeacon("/api/stream/close")` needs beacon semantics `post()` can't give it.

## Non-obvious spec constraints

- Two separate pages, **not** a SPA. Browser back must return to the file browser — don't merge the routes.
- Per-directory cursor memory is spec, not polish (`mp.cursor`).
- Mount keybinds are positional: `1`–`9` → mounts 0–8, `0` → the tenth. `MaxMounts` (10) exists because that's how many keys there are.
- Sort default is `ctime_desc`, and folders always sort before files regardless of key.
- Icons are Nerd Font glyphs, needing a Nerd Font on the *client*.
- `ctime` needs a `syscall.Stat_t`, so `internal/api/ctime_linux.go`/`ctime_other.go` are build-tagged (non-Linux falls back to mtime). Keep both in sync when touching `FileEntry`.
- Mount edits are **live**: `/api/config` POST and the TUI both call `config.Replace`. Handlers must read mounts through `Snapshot()`/`MountByIndex()` and never cache them. `Replace` touches only mounts, so `disk` survives it — but `Save()` marshals the whole struct, so any save writes a `"disk"` key into configs that lacked one.
- `/api/stream/open` **ignores** a `t` (start seconds) param and `api.js` no longer sends one: with VOD playlists the client seeks via standard HLS instead of re-spawning at an offset. The handler still documents it as accepted-and-ignored so an old cached page can't break; don't reintroduce a dependency on it. The `t` on the `/player` **page** URL is a different, live parameter (sheet deep links) that never reaches the server — the names collide, nothing connects them.
- Keybind collisions are easy to miss because the two pages have separate handlers. `q` is the cautionary tale: it closed the player on `/player` while doing something unrelated on `/`, and is now unbound in the file list (the sheet claims it). `Backspace` is likewise unbound there and is *not* an alias for "up one directory" — `h`/`←` do that. Check the `README.md` tables, not one handler, before claiming a key is free.
