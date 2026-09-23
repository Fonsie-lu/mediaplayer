(function () {
  const params = new URLSearchParams(location.search);
  const mount = params.get("mount");
  const path = params.get("path");
  const video = document.getElementById("video");
  const stage = document.getElementById("stage");
  const statusEl = document.getElementById("status");
  const osdEl = document.getElementById("osd");
  const helpEl = document.getElementById("help");
  const qualitySel = document.getElementById("quality");
  const audioSel = document.getElementById("audio");
  const audioItem = document.getElementById("audio-item");
  const muteBtn = document.getElementById("mute");
  const helpBtn = document.getElementById("help-btn");
  const helpCloseBtn = document.getElementById("help-close");
  const closeBtn = document.getElementById("close");

  let hls = null;
  let mode = "none"; // 'direct' | 'hls'
  let probe = null;
  let currentQuality = "auto";
  let currentAudio = 0;
  let keepalive = null;
  let playlistURL = null;
  // Bumped by every play(). play() awaits the network, so a second call (a
  // quick quality change, an error fallback) can start while the first is
  // still waiting; each step checks its generation and a stale one stops
  // instead of attaching a playlist the server has already replaced.
  let playGen = 0;
  // Set once direct playback has failed in this browser. The server's
  // `direct` verdict is browser-agnostic — Safari has no Matroska, some
  // builds lack a codec — so the page's own attempt is the final word.
  let directFailed = false;
  // Recovery budgets for the current stream, refilled whenever a fragment
  // lands, so a stream that keeps failing gives up instead of looping.
  let reopens = 0;
  let mediaRecoveries = 0;
  // Where the current play() was asked to start. A failure before playback
  // gets going leaves currentTime at 0, and a retry from there would throw
  // the resume position away.
  let currentStart = 0;
  function resumePoint() {
    return video.currentTime > 0 ? video.currentTime : currentStart;
  }
  // The in-flight /api/stream/open, aborted when a newer play() supersedes it.
  let openAbort = null;
  // Set by closeAndLeave once it has saved the position: the teardown and the
  // pagehide that follow see a reset playhead and must not write over it.
  let leaving = false;

  // ---------- Resume positions ----------
  // localStorage map `mp.resume`: "<mount>:<path>" -> {t, dur, ts}.
  // The browser page reads the same map to render progress markers.
  const RESUME_KEY = "mp.resume";
  const RESUME_MAX = 200;
  const resumeId = `${mount}:${path}`;

  function loadResumeMap() {
    try {
      return JSON.parse(localStorage.getItem(RESUME_KEY) || "{}");
    } catch (_) {
      return {};
    }
  }
  function storedResume() {
    const e = loadResumeMap()[resumeId];
    return e && e.t > 0 ? e.t : 0;
  }
  // `?t=` in the *page* URL (the browser page's thumbnail sheet links every
  // frame with one) is an explicit "start here" and outranks the stored resume
  // position. Unrelated to the legacy `t` on /api/stream/open, which the server
  // ignores — this one never leaves the client.
  function requestedStart() {
    const t = parseFloat(params.get("t"));
    return isFinite(t) && t > 0 ? t : 0;
  }
  function initialStart() {
    return requestedStart() || storedResume();
  }
  // How the status line describes a non-zero start. Only the first play() can
  // be a `?t=` jump; the later ones (quality / audio switches) pick up the live
  // playhead, which is a resume in every sense.
  let startLabel = requestedStart() ? "from" : "resumed";
  // Records the playhead; positions near the start or end clear the entry
  // so finished videos restart from the beginning next time.
  function rememberPosition(t, dur) {
    const m = loadResumeMap();
    if (!dur || !isFinite(dur) || t < 10 || t > dur * 0.95) {
      if (!m[resumeId]) return;
      delete m[resumeId];
    } else {
      m[resumeId] = { t: Math.floor(t), dur: Math.round(dur), ts: Date.now() };
      const keys = Object.keys(m);
      if (keys.length > RESUME_MAX) {
        keys.sort((a, b) => (m[a].ts || 0) - (m[b].ts || 0));
        keys.slice(0, keys.length - RESUME_MAX).forEach((k) => delete m[k]);
      }
    }
    try {
      localStorage.setItem(RESUME_KEY, JSON.stringify(m));
    } catch (_) {}
  }
  function videoDuration() {
    if (isFinite(video.duration) && video.duration > 0) return video.duration;
    return probe ? probe.duration || 0 : 0;
  }

  function setStatus(msg, err) {
    statusEl.textContent = msg || "";
    statusEl.className = "status" + (err ? " err" : "");
  }

  // Transient overlay over the video. The native controls auto-hide, so a
  // keyboard seek or volume change would otherwise have no visible effect.
  let osdTimer = null;
  function osd(text, ms = 1100) {
    osdEl.textContent = text;
    osdEl.hidden = false;
    clearTimeout(osdTimer);
    osdTimer = setTimeout(() => {
      osdEl.hidden = true;
    }, ms);
  }

  // Autoplay is refused without a user gesture in most browsers (the click
  // that opened this page was on another document). The video then just sits
  // paused, so say what to do instead of failing silently.
  function startPlayback() {
    video.play().catch((e) => {
      if (e && e.name === "NotAllowedError") osd("▶ press space to play", 5000);
    });
  }

  function canNativeHLS() {
    return video.canPlayType("application/vnd.apple.mpegurl") !== "";
  }

  async function init() {
    if (!mount || !path) {
      setStatus("missing mount or path", true);
      return;
    }
    try {
      probe = await api.probe(mount, path);
    } catch (e) {
      setStatus("probe failed: " + e.message, true);
      return;
    }
    qualitySel.value = probe.direct ? "source" : "auto";
    currentQuality = qualitySel.value;
    currentAudio = probe.preferred_audio || 0;
    renderAudioTracks();
    await play(initialStart());
    startLabel = "resumed";
  }

  function trackLabel(t) {
    const lang = t.language || "und";
    const name = t.title ? `${lang} · ${t.title}` : lang;
    return `${name} (${t.codec || "?"})`;
  }

  function renderAudioTracks() {
    const tracks = probe.audio_tracks || [];
    if (tracks.length < 2) {
      audioSel.innerHTML = "";
      audioItem.hidden = true;
      return;
    }
    audioSel.innerHTML = "";
    tracks.forEach((t) => {
      const o = document.createElement("option");
      o.value = String(t.index);
      o.textContent = trackLabel(t);
      audioSel.appendChild(o);
    });
    audioSel.value = String(currentAudio);
    audioItem.hidden = false;
  }

  // Direct playback always plays the container's default audio track, so a
  // non-default selection forces the HLS path (cheap: h264 sources remux).
  function audioOverridden() {
    return (
      (probe.audio_tracks || []).length > 1 &&
      currentAudio !== (probe.preferred_audio || 0)
    );
  }

  async function play(startSec) {
    // Before tearDown: its load() can fire a timeupdate, whose resume write
    // must already see where this play() is headed.
    currentStart = startSec;
    const gen = ++playGen;
    tearDown();
    const resumeNote =
      startSec > 0 ? ` · ${startLabel} ${fmtTime(startSec)}` : "";
    const wantDirect =
      probe.direct &&
      !directFailed &&
      (currentQuality === "source" || currentQuality === "auto") &&
      !audioOverridden();
    if (wantDirect) {
      mode = "direct";
      video.src = api.directURL(mount, path);
      video.addEventListener(
        "loadedmetadata",
        () => {
          if (gen !== playGen) return;
          // A container the browser can demux but a video codec it can't
          // decode plays as audio only in some engines, with no error event.
          if (probe.width > 0 && video.videoWidth === 0) {
            fallBackFromDirect("no picture");
            return;
          }
          if (startSec > 0) video.currentTime = startSec;
          pickEnglishAudioTrack();
        },
        { once: true },
      );
      startPlayback();
      setStatus(
        `direct · ${probe.vcodec}/${probe.acodec || "-"} · ${probe.width}x${probe.height}` +
          resumeNote,
      );
      return;
    }
    mode = "hls";
    const q =
      currentQuality === "auto" || currentQuality === "source"
        ? ""
        : currentQuality;
    let info;
    openAbort = new AbortController();
    try {
      info = await api.openStream(
        mount,
        path,
        q,
        currentAudio,
        openAbort.signal,
      );
    } catch (e) {
      // An abort is this page superseding or leaving the open, not a failure.
      if (gen === playGen && e.name !== "AbortError")
        setStatus("open stream failed: " + e.message, true);
      return;
    }
    if (gen !== playGen) return;
    attachHLS(info.playlist, startSec, gen);
    // While paused, no segment requests reach the server and the idle
    // reaper would kill the session after 10 min. Periodic playlist
    // fetches keep it alive; teardown stops them so dead tabs still reap.
    playlistURL = info.playlist;
    keepalive = setInterval(() => {
      fetch(playlistURL, { credentials: "same-origin" }).catch(() => {});
    }, 240000);
    setStatus(
      `${info.mode || "transcode"} · ${q || "auto"} · ${fmtTime(info.duration || 0)}` +
        resumeNote +
        (directFailed ? " · direct play unsupported here" : ""),
    );
  }

  // The server can only guess what this browser decodes; when the guess was
  // wrong, stream the same position through HLS instead (remux when the
  // video allows it, so usually no transcode).
  function fallBackFromDirect(why) {
    if (mode !== "direct" || directFailed) return;
    directFailed = true;
    const t = resumePoint();
    setStatus(`direct playback failed (${why}) — switching to HLS…`);
    play(t);
  }

  // Resume where the playhead is, through a fresh server session. For when
  // the old one is gone: reaped, closed by a pagehide this bfcache'd page
  // came back from, or lost to a server restart (the TUI's ctrl+r).
  function reopen() {
    if (reopens >= 2) return false;
    reopens++;
    setStatus("stream session lost — reopening…");
    play(resumePoint());
    return true;
  }

  // hls.js tuning, all for a VOD playlist whose segments are made on demand:
  //
  //   * startPosition — load the fragment at the resume point first. Without
  //     it hls.js fetches segment 0, the server spawns a batch there, and the
  //     seek that follows loadedmetadata kills that batch for one at the real
  //     position: a wasted ffmpeg start on every resume, sheet deep link and
  //     quality or audio switch.
  //   * fragLoadPolicy — the server holds a segment request open until the
  //     segment exists: a fresh batch first stops the old one, probes where
  //     its seek lands, spawns and encodes, and the wait for the segment
  //     alone may take 30s. hls.js's 10s time-to-first-byte default aborted
  //     slow ones and re-requested them, and a few in a row went fatal.
  //   * backBufferLength — played-out media otherwise stays in the
  //     SourceBuffer for the whole film; a high-bitrate remux hits the
  //     browser's quota (and a phone's memory) long before the end.
  //   * maxBufferHole — the media a segment carries can begin a few tens of
  //     milliseconds after the playlist position it is advertised at, so a
  //     seek landing right on a boundary can put the playhead just ahead of
  //     the data. The server leaves each batch's first segment a lead for
  //     exactly this (transcode.segmentLead) and that covers the seeks that
  //     would otherwise cost an ffmpeg batch; this covers the remainder —
  //     segments inside a batch, and remux, which gets no lead. At the 0.1s
  //     default such a gap reads as a hole to fill by fetching the *previous*
  //     segment; wider, hls.js skips the few ms instead.
  function hlsConfig(startSec) {
    return {
      startPosition: startSec > 0 ? startSec : -1,
      backBufferLength: 30,
      maxBufferHole: 0.5,
      fragLoadPolicy: {
        default: {
          maxTimeToFirstByteMs: 40000,
          maxLoadTimeMs: 120000,
          timeoutRetry: { maxNumRetry: 2, retryDelayMs: 0, maxRetryDelayMs: 0 },
          errorRetry: {
            maxNumRetry: 4,
            retryDelayMs: 1000,
            maxRetryDelayMs: 8000,
          },
        },
      },
    };
  }

  function attachHLS(url, startSec, gen) {
    const Hls = window.Hls;
    if (Hls && Hls.isSupported()) {
      hls = new Hls(hlsConfig(startSec));
      hls.on(Hls.Events.FRAG_BUFFERED, () => {
        reopens = 0;
        mediaRecoveries = 0;
      });
      hls.on(Hls.Events.ERROR, (_, data) => {
        if (!data.fatal || gen !== playGen) return;
        // 404 is the session, not the network: the server no longer knows
        // this sid. hls.js doesn't retry 4xx, so this arrives at once.
        if (data.response && data.response.code === 404 && reopen()) return;
        if (data.type === Hls.ErrorTypes.MEDIA_ERROR && mediaRecoveries < 2) {
          mediaRecoveries++;
          hls.recoverMediaError();
          return;
        }
        setStatus("hls fatal: " + data.details, true);
      });
      hls.loadSource(url);
      hls.attachMedia(video);
      startPlayback();
    } else if (canNativeHLS()) {
      // Native HLS (Safari) has no start-position hint; seek once the
      // playlist's timeline is known.
      video.src = url;
      if (startSec > 0) {
        video.addEventListener(
          "loadedmetadata",
          () => {
            if (gen === playGen) video.currentTime = startSec;
          },
          { once: true },
        );
      }
      startPlayback();
    } else if (window.__hlsjsFailed) {
      setStatus(
        "hls.js failed to load and browser lacks native HLS — vendor hls.js offline",
        true,
      );
    } else {
      setStatus("HLS unsupported in this browser", true);
    }
  }

  // Element-level errors. hls.js reports through its own event, so this is
  // only for the paths where the browser fetches by itself: a direct file it
  // turns out not to support, and native HLS losing its session.
  video.addEventListener("error", () => {
    const err = video.error;
    if (!err) return;
    if (mode === "direct") {
      if (
        err.code === MediaError.MEDIA_ERR_SRC_NOT_SUPPORTED ||
        err.code === MediaError.MEDIA_ERR_DECODE
      ) {
        fallBackFromDirect(
          err.code === MediaError.MEDIA_ERR_DECODE
            ? "decode error"
            : "unsupported",
        );
      }
      return;
    }
    if (mode === "hls" && !hls && !reopen()) {
      setStatus("playback error: " + (err.message || err.code), true);
    }
  });
  // Native HLS has no FRAG_BUFFERED to refill the reopen budget on; playback
  // actually running is the same signal.
  video.addEventListener("playing", () => {
    if (!hls) reopens = 0;
  });

  // Prefer an English audio track if the source has multiple. HLS output is
  // already filtered server-side to a single track, so this only matters for
  // direct playback. Browser support for HTMLMediaElement.audioTracks is
  // uneven (Safari yes, Firefox behind a pref, Chrome effectively no) — we
  // try and silently no-op where the API is missing.
  function pickEnglishAudioTrack() {
    const tracks = video.audioTracks;
    if (!tracks || tracks.length <= 1) return;
    const isEnglish = (lang) => {
      if (!lang) return false;
      const l = String(lang).toLowerCase();
      return l === "en" || l === "eng" || l.startsWith("en-");
    };
    let target = -1;
    for (let i = 0; i < tracks.length; i++) {
      if (isEnglish(tracks[i].language)) {
        target = i;
        break;
      }
    }
    if (target < 0) return;
    for (let i = 0; i < tracks.length; i++) {
      tracks[i].enabled = i === target;
    }
  }

  function tearDown() {
    if (openAbort) {
      openAbort.abort();
      openAbort = null;
    }
    if (keepalive) {
      clearInterval(keepalive);
      keepalive = null;
    }
    if (hls) {
      try {
        hls.destroy();
      } catch (_) {}
      hls = null;
    }
    mode = "none";
    // load() with no source also clears video.error and drops any error
    // event still queued for the old source, so the element-level error
    // handler can't act on a stream that is already gone.
    video.removeAttribute("src");
    video.load();
  }

  async function closeAndLeave() {
    rememberPosition(resumePoint(), videoDuration());
    leaving = true;
    tearDown();
    try {
      await api.closeStream();
    } catch (_) {}
    history.length > 1 ? history.back() : (location.href = "/");
  }

  // VOD playlist + on-demand segment generation means seeks are handled
  // natively by hls.js (or the browser for direct mode) — no special
  // restart logic needed. Server transcodes ~1 min ahead of the requested
  // segment and bounded behind, so scrubbing anywhere on the timeline just
  // triggers normal segment fetches.

  // ---------- UI ----------
  qualitySel.addEventListener("change", async () => {
    currentQuality = qualitySel.value;
    const srcTime = resumePoint();
    await play(srcTime);
  });

  audioSel.addEventListener("change", async () => {
    currentAudio = parseInt(audioSel.value, 10) || 0;
    const srcTime = resumePoint();
    await play(srcTime);
  });

  // Persist the playhead (throttled) so the browser page can show progress
  // and the next open resumes where playback left off.
  let lastResumeSave = 0;
  video.addEventListener("timeupdate", () => {
    const now = Date.now();
    if (leaving || now - lastResumeSave < 3000) return;
    lastResumeSave = now;
    rememberPosition(resumePoint(), videoDuration());
  });
  video.addEventListener("ended", () =>
    rememberPosition(videoDuration(), videoDuration()),
  );

  const muteIcon = muteBtn.querySelector(".nav-ic") || muteBtn;
  function renderMute() {
    muteIcon.textContent = video.muted ? "🔇" : "🔊";
  }
  muteBtn.addEventListener("click", () => toggleMute());
  video.addEventListener("volumechange", renderMute);
  renderMute();

  closeBtn.addEventListener("click", closeAndLeave);
  // Coming back to a page closeAndLeave left (bfcache): it plays again, so
  // its position is worth saving again.
  window.addEventListener("pageshow", (ev) => {
    if (ev.persisted) leaving = false;
  });
  window.addEventListener("pagehide", () => {
    if (!leaving) rememberPosition(resumePoint(), videoDuration());
    // best-effort cleanup on back nav or tab close
    try {
      navigator.sendBeacon("/api/stream/close");
    } catch (_) {}
  });

  // ---------- Actions ----------

  function togglePlay() {
    if (video.paused) {
      video.play().catch(() => {});
      osd("▶");
    } else {
      video.pause();
      osd("⏸");
    }
  }

  // Seeks clamp to just short of the end: landing exactly on the duration
  // fires `ended`, which rememberPosition() reads as "finished" and wipes the
  // resume entry.
  function seekTo(t, label) {
    const dur = videoDuration();
    let to = Math.max(0, t);
    if (dur > 0) to = Math.min(to, Math.max(0, dur - 0.5));
    video.currentTime = to;
    const of = dur > 0 ? ` / ${fmtTime(dur)}` : "";
    osd(`${label} ${fmtTime(to)}${of}`);
  }

  function seekBy(delta) {
    const sign = delta > 0 ? "+" : "−";
    seekTo(
      (video.currentTime || 0) + delta,
      `${delta > 0 ? "⏩" : "⏪"} ${sign}${fmtTime(Math.abs(delta))} ·`,
    );
  }

  function seekToFraction(f) {
    const dur = videoDuration();
    if (dur <= 0) return;
    seekTo(dur * f, `${Math.round(f * 100)}% ·`);
  }

  function volumeBy(delta) {
    const v = Math.min(1, Math.max(0, (video.volume || 0) + delta));
    video.volume = v;
    // Nudging the volume up while muted should be audible, not silent.
    if (v > 0 && video.muted) video.muted = false;
    osd(v === 0 ? "🔇 0%" : `🔊 ${Math.round(v * 100)}%`);
  }

  function toggleMute() {
    video.muted = !video.muted;
    renderMute();
    osd(video.muted ? "🔇 muted" : `🔊 ${Math.round(video.volume * 100)}%`);
  }

  function toggleFullscreen() {
    if (document.fullscreenElement) {
      document.exitFullscreen().catch(() => {});
      return;
    }
    // Fullscreen the stage, not the video: only the fullscreen element's
    // subtree renders, so fullscreening the bare <video> would hide the OSD
    // and help card. iOS Safari has no element fullscreen — it only offers
    // the video's own native fullscreen.
    if (stage.requestFullscreen) {
      stage.requestFullscreen().catch(() => {});
    } else if (video.webkitEnterFullscreen) {
      video.webkitEnterFullscreen();
    }
  }

  function setHelp(open) {
    helpEl.hidden = !open;
  }

  // ---------- Keyboard ----------
  //
  // One handler, on window, in the CAPTURE phase. Both parts matter:
  //
  //   * Capture — a click on the video (or its timeline) puts focus inside the
  //     native controls' shadow DOM, which handles arrows/space itself and can
  //     consume the keydown before a bubble-phase listener ever runs. Capturing
  //     at the window means we see every key first, whatever holds focus.
  //   * stopImmediatePropagation on keys we own — otherwise the controls also
  //     act on them and a single press seeks twice, or `space` both toggles
  //     play and re-clicks whichever nav button was last clicked.
  const actions = {
    " ": togglePlay,
    k: () => seekBy(60),
    j: () => seekBy(-60),
    l: () => seekBy(5),
    ArrowRight: () => seekBy(5),
    h: () => seekBy(-5),
    ArrowLeft: () => seekBy(-5),
    ArrowUp: () => volumeBy(0.1),
    ArrowDown: () => volumeBy(-0.1),
    m: toggleMute,
    f: toggleFullscreen,
    "?": () => setHelp(helpEl.hidden),
  };
  for (let d = 0; d <= 9; d++) {
    actions[String(d)] = () => seekToFraction(d / 10);
  }

  // Keys a focused <select> needs to operate itself. Everything else still
  // reaches the player, so the shortcuts keep working after using the quality
  // or audio dropdown — the old handler bailed out on any focused SELECT,
  // which left the keyboard dead until the user clicked elsewhere.
  const selectOwns = new Set([
    "ArrowUp",
    "ArrowDown",
    "ArrowLeft",
    "ArrowRight",
    " ",
    "Enter",
    "Escape",
    "Home",
    "End",
    "PageUp",
    "PageDown",
  ]);

  function keyHandler(ev) {
    // Let the browser keep its own shortcuts (ctrl+R, cmd+L, alt+←, …).
    if (ev.ctrlKey || ev.metaKey || ev.altKey) return;
    const el = document.activeElement;
    const tag = el && el.tagName;
    if (tag === "INPUT" || tag === "TEXTAREA" || (el && el.isContentEditable)) {
      return;
    }
    const isSelect = tag === "SELECT";
    if (isSelect && selectOwns.has(ev.key)) return;

    // Help is a layer on top: closing it takes priority over leaving the page.
    if (!helpEl.hidden && (ev.key === "Escape" || ev.key === "q")) {
      ev.preventDefault();
      ev.stopImmediatePropagation();
      setHelp(false);
      return;
    }
    if (
      ev.key === "q" ||
      (ev.key === "Escape" && !document.fullscreenElement)
    ) {
      ev.preventDefault();
      ev.stopImmediatePropagation();
      closeAndLeave();
      return;
    }
    const action = actions[ev.key];
    if (!action) return;
    ev.preventDefault();
    ev.stopImmediatePropagation();
    action();
  }

  window.addEventListener("keydown", keyHandler, true);
  // Belt-and-suspenders: also bind on the video itself in the capture phase so
  // the key is intercepted even if a browser routes it directly to the focused
  // media element before window capture (some engines do for media controls).
  // Window capture runs first and stops propagation, so this never double-fires.
  video.addEventListener("keydown", keyHandler, true);

  // Clicking the native controls (play button, timeline) puts focus inside the
  // browser's closed user-agent shadow root, and from there keydown never
  // reaches the page at all: not the document, not even a capture-phase
  // listener on window. Every shortcut stays dead until focus leaves — the
  // reason `q` and `Esc` used to stop working after touching the scrubber.
  //
  // No pointer event escapes that shadow root either, so `focusin` is the only
  // signal available. Pointer-driven focus is handed straight back to the
  // document; `:focus-visible` is true only for keyboard-driven focus, so
  // tabbing to the video still works normally. Browsers without
  // :focus-visible throw on matches() — treat that as "blur", which keeps the
  // shortcuts alive at the cost of not being able to tab into the controls.
  video.addEventListener("focusin", () => {
    // Blurring synchronously inside the focusin dispatch has no effect — the
    // browser is still assigning focus and simply re-applies it. Defer a tick.
    setTimeout(() => {
      if (document.activeElement !== video) return;
      let keyboardDriven = false;
      try {
        keyboardDriven = video.matches(":focus-visible");
      } catch (_) {}
      if (!keyboardDriven) video.blur();
    }, 0);
  });

  helpBtn.addEventListener("click", () => setHelp(helpEl.hidden));
  helpCloseBtn.addEventListener("click", () => setHelp(false));
  // Clicking the dimmed backdrop (but not the card) dismisses the help.
  helpEl.addEventListener("click", (ev) => {
    if (ev.target === helpEl) setHelp(false);
  });

  init();
})();
