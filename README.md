# mediaplayer

A self-hosted, single-binary web media player and file browser for your video library. Point it at one or more directories on a server and stream their contents to any browser — with native direct playback when the browser can handle the file, and on-the-fly HLS remuxing or transcoding when it can't.

The Go backend is **stdlib-only** and embeds the entire frontend (`//go:embed all:web`), so deployment is a single static binary plus `ffmpeg` on the host. The frontend is vanilla JS — no build step, no framework.

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
- **Thumbnail previews** via `ffmpegthumbnailer`.
- **Tokyo Night** theme.

## Requirements

The following must be on the host's `PATH`:

| Tool                | Purpose                                           |
| ------------------- | ------------------------------------------------- |
| `ffmpeg`            | HLS remux / transcode                             |
| `ffprobe`           | Codec/container detection for the stream decision |
| `ffmpegthumbnailer` | 300px PNG previews                                |

[`hls.js`](https://github.com/video-dev/hls.js) is loaded from the jsDelivr CDN in `web/player.html`. For offline / air-gapped deployments, vendor it into `web/vendor/` and update the `<script src>`. Safari has native HLS and doesn't need it.

A **Nerd Font** must be installed on the _client_ for the file-browser icon glyphs to render.

Go 1.22+ is needed to build.

## Build & run

```bash
make build          # go build -o mediaplayer .
make run            # go run .
make test           # go test -v ./...
make clean          # remove binary and leftover /tmp transcode dirs
```

```bash
./mediaplayer                          # uses ~/.config/mediaplayer.json
./mediaplayer -config /path/config.json
```

On first run with no config file, a default one is written to `~/.config/mediaplayer.json` (honoring `XDG_CONFIG_HOME`).

## Configuration

```json
{
  "host": "0.0.0.0",
  "port": 8090,
  "mounts": [{ "name": "home", "path": "/home/fonsie/vid/" }]
}
```

- `host` / `port` — listen address (defaults `0.0.0.0:8090`).
- `mounts` — up to 10 named directory roots. Mount paths can also be edited at runtime via the `/api/config` endpoint (changes are persisted back to the file).

All filesystem access is sandboxed under the mount roots: user-supplied relative paths are cleaned and re-rooted, so `../../../etc` collapses to a path _inside_ the mount rather than escaping it.

## How it works

The server exposes two pages and a small JSON/HLS API:

- `GET /` — file browser (`web/browser.html`)
- `GET /player` — player (`web/player.html`); kept on a distinct URL so browser **back** returns to the browser
- `GET /css/*`, `/js/*` — embedded static assets
- `/api/*` — JSON API: `mounts`, `browse`, `rename`, `delete`, `preview`, `probe`, `config`, and the streaming endpoints (`stream/direct`, `stream/open`, `stream/close`, `stream/hls/{sid}/...`)

One transcode session is tracked per client cookie (`mp_sid`). Sessions stream HLS segments in bounded on-demand batches (kept in a tmpfs window around the playhead), are touched on every request as a keepalive, and are reaped after 10 minutes idle. Leftover temp dirs from crashed runs are cleaned at startup, and `SIGINT`/`SIGTERM` tears down all live sessions before exit.

For a detailed walkthrough of the stream decision flow, HLS batching, ffmpeg invocation, and session lifecycle, see [`CLAUDE.md`](CLAUDE.md).

## Project layout

```
main.go                  entrypoint, embed, routing, signal handling
internal/config/         config load/save, mounts
internal/api/            HTTP handlers, path safety, browse/stream/preview
internal/session/        per-cookie session manager, segment batching, reaper
internal/transcode/      ffprobe, keyframe scan, HLS batch (remux/encode)
web/                     embedded frontend (HTML, vanilla JS, CSS)
```

## License

No license file is currently included; add one before distributing.
