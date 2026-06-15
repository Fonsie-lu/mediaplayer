(function () {
  const params = new URLSearchParams(location.search);
  const mount = params.get("mount");
  const path = params.get("path");
  const video = document.getElementById("video");
  const statusEl = document.getElementById("status");
  const qualitySel = document.getElementById("quality");
  const audioSel = document.getElementById("audio");
  const audioItem = document.getElementById("audio-item");
  const muteBtn = document.getElementById("mute");
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
    await play(storedResume());
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
    const resumeNote = startSec > 0 ? ` · resumed ${fmtTime(startSec)}` : "";
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
        info = await api.openStream(mount, path, 0, q, currentAudio);
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
  muteBtn.addEventListener("click", () => {
    video.muted = !video.muted;
    renderMute();
  });
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

  // Close keys are handled in the capture phase on window so they fire no
  // matter where focus landed — clicking the native video timeline moves focus
  // into the controls' shadow DOM, which would otherwise swallow the keydown
  // before the document-level handler below sees it.
  function closeKeyHandler(ev) {
    const tag = document.activeElement && document.activeElement.tagName;
    if (tag === "INPUT" || tag === "TEXTAREA") return;
    if (ev.key === "q" || ev.key === "Escape") {
      ev.preventDefault();
      ev.stopImmediatePropagation();
      closeAndLeave();
    }
  }
  window.addEventListener("keydown", closeKeyHandler, true);
  // Belt-and-suspenders: also bind on the video itself in the capture phase so
  // the key is intercepted even if a browser routes it directly to the focused
  // media element before window capture (some engines do for media controls).
  video.addEventListener("keydown", closeKeyHandler, true);

  document.addEventListener("keydown", (ev) => {
    if (
      ["INPUT", "SELECT", "TEXTAREA"].includes(document.activeElement.tagName)
    )
      return;
    switch (ev.key) {
      case " ":
        video.paused ? video.play() : video.pause();
        ev.preventDefault();
        break;
      case "m":
        muteBtn.click();
        break;
      case "f":
        if (document.fullscreenElement) document.exitFullscreen();
        else video.requestFullscreen();
        break;
      case "ArrowLeft":
      case "h":
        video.currentTime = Math.max(0, video.currentTime - 5);
        ev.preventDefault();
        break;
      case "ArrowRight":
      case "l":
        video.currentTime = video.currentTime + 5;
        ev.preventDefault();
        break;
      case "j":
        video.currentTime = Math.max(0, video.currentTime - 30);
        ev.preventDefault();
        break;
      case "k":
        video.currentTime = video.currentTime + 30;
        ev.preventDefault();
        break;
    }
  });

  init();
})();
