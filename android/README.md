# Android app

A WebView wrapper around the web client, so a phone gets a launcher icon and a
full-screen window instead of a browser tab. It renders exactly the same pages
the server already serves — there is no second client to keep in sync, and a
change to `web/` shows up in the app on the next reload.

## Install

Copy `mediaplayer.apk` (repo root) to the phone and open it. Android will ask
you to allow installs from whatever app you copied it with; the APK is
self-signed, so there is no Play Store involved.

On first launch it asks for the server address (`http://<host>:8090`, or just
`host:8090` — the scheme is filled in). It's stored, so later launches go
straight to the file browser. Change it any time from the ⋮ menu → **Server…**,
which is also offered when a page fails to load.

The phone has to reach the server, so bind it to something other than
localhost (`"host": "0.0.0.0"` in the config) and use the machine's LAN
address.

## What the wrapper adds

- **Fullscreen video** — the player requests fullscreen on `#stage`, which
  arrives as a WebChromeClient custom view; the app hides its action bar, goes
  immersive, and rotates to landscape, then restores all three on exit. Back
  leaves fullscreen before it leaves the page.
- **Keep-awake while playing** — the page never asks Android for a wakelock, so
  a small script injected on each page load reports `play`/`pause`/`ended`
  through a `MPHost` bridge and the window's KEEP_SCREEN_ON flag follows.
- **DOM storage and cookies** — per-directory cursor memory, sort preference
  and resume positions live in `localStorage`, and the `mp_sid` session cookie
  is what ties a browser to its transcode session. Both are enabled explicitly.
- **Autoplay** — `setMediaPlaybackRequiresUserGesture(false)`, or HLS playback
  stalls waiting for a gesture that already happened.
- **Links out** stay out: anything not on the configured origin is handed to
  the system browser.

Known cosmetic gap: the file-list icons are Nerd Font glyphs, and Android has
no Nerd Font, so they render as placeholder boxes. Everything else — layout,
colours, the disk readout, the mobile nav — renders as it does in a desktop
browser at phone width.

## Build

```bash
./android/build.sh          # writes ./mediaplayer.apk
```

Needs a JDK and an Android SDK (build-tools + one platform); override
`JAVA_HOME`, `ANDROID_HOME`, `BUILD_TOOLS_VERSION`, `PLATFORM`, `OUT` if yours
live elsewhere. There is no Gradle and no Android Gradle Plugin: the app is one
activity with no third-party dependencies, so the script runs aapt2, javac, d8,
zipalign and apksigner directly and needs no network.

The signing key is generated on first build at
`~/.config/mediaplayer-android.jks` (override with `KEYSTORE`). **Keep it** —
Android refuses to install an update signed by a different key, so losing it
means uninstalling the app before installing again.

## Layout

```
android/
  AndroidManifest.xml                       package, permissions, launcher activity
  build.sh                                  the whole build
  res/values/styles.xml                     tokyo-night colours for the native chrome
  res/drawable/, res/mipmap-anydpi-v26/     adaptive launcher icon (vector, no PNGs)
  src/ch/bithawk/mediaplayer/MainActivity.java
```
