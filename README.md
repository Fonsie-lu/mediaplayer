# mediaplayer

A self-hosted, single-binary web media player and file browser for your video library. Point it at one or more directories on a server and stream their contents to any browser — with native direct playback when the browser can handle the file, and on-the-fly HLS remuxing or transcoding when it can't.

The Go backend embeds the entire frontend (`//go:embed all:web`), so deployment is a single static binary plus `ffmpeg` on the host. The web/HTTP layer is stdlib-only; the optional terminal control panel is the lone dependency ([Bubble Tea](https://github.com/charmbracelet/bubbletea)). The frontend is vanilla JS — no build step, no framework.

When launched on a terminal it opens a **TUI control panel** (Mounts / Stars / Logs) over the running server; redirect it to a pipe or pass `-no-tui` for headless operation.

## Features

- **File browser** with vim-style keybindings, per-directory cursor memory, filtering, sorting, rename, and delete.
- **Up to 10 named mounts**, each jumpable by number key (`1`–`9`, `0`).
- **Smart stream decision** per file via `ffprobe`:
  - **Direct** — compatible codec/container served raw with native browser Range/seek (no ffmpeg).
  - **Remux** — h264 video bit-identically copied into HLS segments; only incompatible audio is re-encoded.
  - **Transcode** — full HLS transcode for anything else, or any quality cap.
- **Full timeline from the start** — a synthetic VOD playlist enumerates every segment up front, so the whole video is seekable immediately. Segments are generated on demand in bounded batches.
- **Audio-track selection** (per-track codec/language reported by the probe).
- **Resume positions** stored client-side; the file list shows a progress marker for partially watched files.
- **Stars** kept server-side (not in `localStorage`), so the same marks show up on every device.
- **Thumbnail previews** via `ffmpegthumbnailer`, plus a `p`-key **thumbnail sheet**: one frame per 10 minutes of runtime, shown scaled-to-fit in an overlay and never written to disk.
- **Folder preview** — with the cursor on a directory, the preview column lists that directory's contents in the listing's active sort order (up to 200 rows) instead of sitting empty.
- **Disk-usage readout** in the browser header for the filesystem the current directory sits on, hidden when there's nothing useful to show.
- **Terminal control panel** (TUI) over the live server: edit mount points, review/unstar starred entries, and watch logs grouped by session and filename in collapsible groups — all with vim navigation, plus a key to restart the binary.
- **Tokyo Night** theme (web and TUI).

## Requirements

The following must be on the host's `PATH`:

| Tool                | Purpose                                           |
| ------------------- | ------------------------------------------------- |
| `ffmpeg`            | HLS remux / transcode                             |
| `ffprobe`           | Codec/container detection for the stream decision |
| `ffmpegthumbnailer` | 300px previews and the 10-minute thumbnail sheet  |

[`hls.js`](https://github.com/video-dev/hls.js) is loaded from the jsDelivr CDN in `web/player.html`. For offline / air-gapped deployments, vendor it into `web/vendor/` and update the `<script src>`. Safari has native HLS and doesn't need it.

A **Nerd Font** must be installed on the _client_ for the file-browser icon glyphs to render.

Go 1.26+ is needed to build (the `go` directive in `go.mod`).

## Build & run

```bash
make build          # go build -o mediaplayer .
make run            # go run .
make test           # go test -v ./...
make clean          # remove binary + /tmp/mediaplayer-* (session dirs and the preview cache)
```

```bash
./mediaplayer                          # uses ~/.config/mediaplayer.json; opens the TUI on a terminal
./mediaplayer -config /path/config.json
./mediaplayer -no-tui                  # headless (also auto-selected when stdout isn't a TTY)
```

On first run with no config file, a default one is written to `~/.config/mediaplayer.json` (honoring `XDG_CONFIG_HOME`).

### Terminal UI

When attached to a terminal, `./mediaplayer` runs the HTTP server in the background and presents a control panel. Tabs are switched with `tab` / `1`·`2`·`3`, and every list uses `j`/`k`, `g`/`G`:

- **Mounts** — `a` add, `e`/`enter` edit, `d` delete (name + path form; persisted to the config file and applied live).
- **Stars** — review starred entries grouped by mount; `d`/`x` to unstar. New stars are still created by browsing files in the web UI.
- **Logs** — server logs grouped by session and filename, each group collapsible (`l`/`h`, `enter`/`space`, `E`/`C` for all) and showing its entry count; `c` clears.

`ctrl+r` restarts the executable (graceful shutdown, then re-exec); `q` quits.

### Android app

`mediaplayer.apk` in the repo root is a WebView wrapper around the same web
client, so a phone gets a launcher icon, a full-screen window, landscape
fullscreen video and a screen that stays awake while something is playing. It
asks for the server address on first launch (⋮ → **Server…** to change it), so
one APK works wherever the server lives — bind it to `0.0.0.0` and use the
machine's LAN address.

Rebuild it with `./android/build.sh` (JDK + Android SDK, no Gradle). See
[`android/README.md`](android/README.md).

## Configuration

```json
{
  "host": "0.0.0.0",
  "port": 8090,
  "disk": "/dev/nvme0n1p3",
  "mounts": [{ "name": "home", "path": "/home/fonsie/vid/" }]
}
```

- `host` / `port` — listen address (defaults `0.0.0.0:8090`).
- `disk` — **fallback** for the used-space readout in the file browser's header.
  The readout normally reports the filesystem the directory you're browsing
  lives on, so it follows you across mounts on different disks and needs no
  configuration; this setting is what it shows when there's no such directory
  yet. Either a device node (`/dev/nvme0n1p3`, resolved to its current
  mountpoint) or a directory (`/srv/media`). Omit it and the readout is simply
  hidden until you're inside a mount.
- `mounts` — up to 10 named directory roots. Mount paths can also be edited at runtime via the `/api/config` endpoint (changes are persisted back to the file).

All filesystem access is sandboxed under the mount roots: user-supplied relative paths are cleaned and re-rooted, so `../../../etc` collapses to a path _inside_ the mount rather than escaping it.

## Client key bindings

All keys below work in the browser (the web client). They are vim-flavored; arrow keys mirror `h`/`j`/`k`/`l` where it makes sense. Icons need a Nerd Font installed on the client.

### File browser (`/`)

**Global**

| Key          | Action                                                          |
| ------------ | --------------------------------------------------------------- |
| `Tab`        | Toggle the active column (file list ↔ mounts)                   |
| `1`–`9`, `0` | Jump to mount by index (`1`–`9` = mounts 1–9, `0` = 10th mount) |

**File list (active column)**

| Key                 | Action                                     |
| ------------------- | ------------------------------------------ |
| `j` / `↓`           | Move down                                  |
| `k` / `↑`           | Move up                                    |
| `l` / `→` / `Enter` | Open folder · play file                    |
| `h` / `←`           | Go up one directory                        |
| `gg`                | Jump to top (press `g` twice within 500ms) |
| `G`                 | Jump to bottom                             |
| `r`                 | Rename selected entry                      |
| `d`                 | Delete selected entry (confirm with `y`)   |
| `y`                 | Toggle star on selected entry              |
| `p`                 | Thumbnail sheet — one frame per 10 min     |
| `f` or `/`          | Open / close the filter                    |
| `o`                 | Open the sort dialog                       |

`p` renders one thumbnail per 10 minutes of the selected video and shows them
all in an overlay sized to fit the window. The frames are generated on the spot
and never cached, so a second `p` on the same file renders again (a couple of
seconds for a feature-length recording).

**Every frame is clickable**: it opens the video in the player starting at that
frame's timestamp, so the sheet doubles as a visual seek bar. The keyboard works
too — `Tab` / `Shift+Tab` step between frames and `Enter` plays the focused one.
Clicking outside the sheet or pressing `Esc` or `q` closes it; any other key is
ignored while it's open, so nothing acts on the file list hidden behind it.

| Key / action        | Effect                                    |
| ------------------- | ----------------------------------------- |
| Click a frame       | Play from that timestamp                  |
| `Tab` / `Shift+Tab` | Move between frames                       |
| `Enter` / `Space`   | Play the focused frame                    |
| `q`                 | Close (rendered sheet only)               |
| Click outside       | Close                                     |
| `Esc`               | Close (or cancel a sheet still rendering) |

**Sort shortcuts** (no dialog — capital letter reverses direction)

| Key | Sort                       |
| --- | -------------------------- |
| `m` | Created time, newest first |
| `M` | Created time, oldest first |
| `s` | Size, largest first        |
| `S` | Size, smallest first       |
| `n` | Name, A→Z                  |
| `N` | Name, Z→A                  |

**Mounts column (active column)**

| Key                 | Action                  |
| ------------------- | ----------------------- |
| `j` / `↓`           | Move down               |
| `k` / `↑`           | Move up                 |
| `l` / `→` / `Enter` | Switch to the file list |

**Dialogs (rename / delete / sort / filter)**

| Key     | Action                      |
| ------- | --------------------------- |
| `Enter` | Confirm (apply filter / OK) |
| `y`     | Confirm a delete prompt     |
| `Esc`   | Cancel / close the dialog   |

**Touch / narrow screens**

Below 900px the mounts column collapses and a tap bar (`☰` toggles it, and the
open state is remembered for the session) covers the same actions without a
keyboard: `▲`/`▼` move, `▶` opens, `⇅` sort, `⌕` filter, `✎` rename,
`▦` thumbnail sheet, `✖` delete.

### Player (`/player`)

| Key          | Action                                  |
| ------------ | --------------------------------------- |
| `Space`      | Play / pause                            |
| `l` / `→`    | Seek forward 5s                         |
| `h` / `←`    | Seek back 5s                            |
| `k`          | Seek forward 1 min                      |
| `j`          | Seek back 1 min                         |
| `↑` / `↓`    | Volume ±10% (unmutes when raised)       |
| `1`–`9`, `0` | Jump to 10%–90%, `0` jumps to the start |
| `m`          | Mute / unmute                           |
| `f`          | Toggle fullscreen                       |
| `?`          | Show the shortcut list                  |
| `q` / `Esc`  | Close the player and return to browsing |

Seeks and volume changes flash a brief on-screen readout, since the native
controls auto-hide. `Esc` closes the shortcut list first if it's open, and while
fullscreen it's left to the browser for leaving fullscreen.

## How it works

The server exposes two pages and a small JSON/HLS API:

- `GET /` — file browser (`web/browser.html`)
- `GET /player` — player (`web/player.html`); kept on a distinct URL so browser **back** returns to the browser
- `GET /css/*`, `/js/*` — embedded static assets
- `/api/*` — JSON API: `mounts`, `browse`, `rename`, `delete`, `preview`, `probe`, `disk`, `sheet`, `config`, and the streaming endpoints (`stream/direct`, `stream/open`, `stream/close`, `stream/hls/{sid}/...`)

Three of those endpoints are worth calling out:

- `preview` caches its thumbnails in a stable `$TMPDIR/mediaplayer-previews` dir keyed by path + mtime + size, so they survive restarts.
- `sheet` is the opposite by design: it renders into a per-request temp dir, returns the frames inline as `data:` URIs, and deletes the dir before replying — nothing is kept.
- `disk` reports the filesystem holding the `mount`/`path` it is given, falling back to the configured one when that's absent or unmeasurable. A `/dev/...` config value is resolved to its current mountpoint through `/proc/mounts` first, because `statfs` on a device node would describe `/dev` instead of the disk. Anything unusable (unset, unmounted, non-Linux) comes back as `{"ok": false}` and the header widget stays hidden.

One transcode session is tracked per client cookie (`mp_sid`). Sessions stream HLS segments in bounded on-demand batches (kept in a tmpfs window around the playhead), are touched on every request as a keepalive, and are reaped after 10 minutes idle. Leftover temp dirs from crashed runs are cleaned at startup, and `SIGINT`/`SIGTERM` tears down all live sessions before exit.

For a detailed walkthrough of the stream decision flow, HLS batching, ffmpeg invocation, and session lifecycle, see [`CLAUDE.md`](CLAUDE.md).

## Project layout

```
main.go                  entrypoint, embed, routing, signal handling, TUI/headless launch + restart
internal/config/         config load/save, mounts, disk setting
internal/api/            HTTP handlers, path safety, browse/stream/preview, sheet, stars, disk usage
internal/session/        per-cookie session manager, segment batching, reaper
internal/transcode/      ffprobe, keyframe scan, HLS batch (remux/encode)
internal/applog/         in-memory parsed log sink (session/filename grouping) for the TUI
internal/tui/            Bubble Tea control panel (Mounts / Stars / Logs tabs)
web/                     embedded frontend (HTML, vanilla JS, CSS)
```

## License

No license file is currently included; add one before distributing.
