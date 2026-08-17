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
  function osd(text) {
    osdEl.textContent = text;
    osdEl.hidden = false;
    clearTimeout(osdTimer);
    osdTimer = setTimeout(() => {
      osdEl.hidden = true;
    }, 1100);
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
    tearDown();
    const resumeNote =
      startSec > 0 ? ` · ${startLabel} ${fmtTime(startSec)}` : "";
    const wantDirect =
      probe.direct &&
      (currentQuality === "source" || currentQuality === "auto") &&
      !audioOverridden();
    if (wantDirect) {
      mode = "direct";
      video.src = api.directURL(mount, path);
      video.addEventListener(
        "loadedmetadata",
        () => {
          if (startSec > 0) video.currentTime = startSec;
          pickEnglishAudioTrack();
        },
        { once: true },
      );
      video.play().catch(() => {});
      setStatus(
        `direct · ${probe.vcodec}/${probe.acodec || "-"} · ${probe.width}x${probe.height}` +
          resumeNote,
      );
    } else {
      mode = "hls";
      const q =
        currentQuality === "auto"
          ? ""
          : currentQuality === "source"
            ? ""
            : currentQuality;
      let info;
      try {
        info = await api.openStream(mount, path, q, currentAudio);
      } catch (e) {
        setStatus("open stream failed: " + e.message, true);
        return;
      }
      await attachHLS(info.playlist);
      // While paused, no segment requests reach the server and the idle
      // reaper would kill the session after 10 min. Periodic playlist
      // fetches keep it alive; teardown stops them so dead tabs still reap.
      playlistURL = info.playlist;
      keepalive = setInterval(() => {
        fetch(playlistURL, { credentials: "same-origin" }).catch(() => {});
      }, 240000);
      if (startSec > 0) {
        // VOD playlist exposes the full timeline immediately, so scrubbing
        // to startSec just triggers normal segment fetches.
        const seekWhenReady = () => {
          try {
            video.currentTime = startSec;
          } catch (_) {}
        };
        if (video.readyState >= 1) seekWhenReady();
        else
          video.addEventListener("loadedmetadata", seekWhenReady, {
            once: true,
          });
      }
      setStatus(
        `${info.mode || "transcode"} · ${q || "auto"} · ${fmtTime(info.duration || 0)}` +
          resumeNote,
      );
    }
  }

  async function attachHLS(url) {
    if (window.Hls && window.Hls.isSupported()) {
      hls = new window.Hls({ lowLatencyMode: false, liveSyncDuration: 4 });
      hls.loadSource(url);
      hls.attachMedia(video);
      hls.on(window.Hls.Events.ERROR, (_, data) => {
        if (data.fatal) setStatus("hls fatal: " + data.details, true);
      });
      video.play().catch(() => {});
    } else if (canNativeHLS()) {
      video.src = url;
      video.play().catch(() => {});
    } else if (window.__hlsjsFailed) {
      setStatus(
        "hls.js failed to load and browser lacks native HLS — vendor hls.js offline",
        true,
      );
    } else {
      setStatus("HLS unsupported in this browser", true);
    }
  }

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
    video.removeAttribute("src");
    video.load();
  }

  async function closeAndLeave() {
    rememberPosition(video.currentTime, videoDuration());
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
    const srcTime = video.currentTime || 0;
    await play(srcTime);
  });

  audioSel.addEventListener("change", async () => {
    currentAudio = parseInt(audioSel.value, 10) || 0;
    const srcTime = video.currentTime || 0;
    await play(srcTime);
  });

  // Persist the playhead (throttled) so the browser page can show progress
  // and the next open resumes where playback left off.
  let lastResumeSave = 0;
  video.addEventListener("timeupdate", () => {
    const now = Date.now();
    if (now - lastResumeSave < 3000) return;
    lastResumeSave = now;
    rememberPosition(video.currentTime, videoDuration());
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
  window.addEventListener("pagehide", () => {
    rememberPosition(video.currentTime, videoDuration());
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
