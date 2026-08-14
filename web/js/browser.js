(function () {
  const state = {
    mounts: [],
    mountIdx: 0,
    path: "",
    entries: [],
    filtered: [],
    focus: 0,
    sort: localStorage.getItem("mp.sort") || "ctime_desc",
    filter: "",
    stars: new Set(), // keys: `${mountIdx}:${rel_path}`
    // cursorMemory[`${mountIdx}:${path}`] = focus index
    cursorMemory: JSON.parse(localStorage.getItem("mp.cursor") || "{}"),
    previewReq: 0,
    sheet: null, // frames of the open thumbnail sheet, null when closed
    sheetEntry: null, // the entry those frames came from (click target)
    sheetBusy: false, // a /api/sheet render is in flight
    gState: 0, // for "gg" double-press
    activeCol: "files", // "files" | "mounts" — which column Tab is on
    focusBeforeFilter: 0, // restore this on filter ESC
  };

  const el = {
    grid: document.getElementById("grid"),
    mountList: document.getElementById("mount-list"),
    fileList: document.getElementById("file-list"),
    colMounts: document.getElementById("col-mounts"),
    colFiles: document.getElementById("col-files"),
    crumbs: document.getElementById("crumbs"),
    filter: document.getElementById("filter"),
    filterInput: document.getElementById("filter-input"),
    sheet: document.getElementById("sheet"),
    sheetHead: document.getElementById("sheet-head"),
    sheetGrid: document.getElementById("sheet-grid"),
    disk: document.getElementById("disk"),
    diskIc: document.getElementById("disk-ic"),
    diskFill: document.getElementById("disk-fill"),
    diskPct: document.getElementById("disk-pct"),
    previewImg: document.getElementById("preview-img"),
    previewMeta: document.getElementById("preview-meta"),
    status: document.getElementById("statusbar"),
    modal: document.getElementById("modal"),
    modalTitle: document.getElementById("modal-title"),
    modalBody: document.getElementById("modal-body"),
    modalOk: document.getElementById("modal-ok"),
    modalCancel: document.getElementById("modal-cancel"),
    mobileNav: document.getElementById("mobile-nav"),
    mobileNavToggle: document.getElementById("mobile-nav-toggle"),
  };

  function status(msg, kind) {
    el.status.textContent = msg || "";
    el.status.className = "statusbar" + (kind ? " " + kind : "");
  }

  function saveCursor() {
    state.cursorMemory[`${state.mountIdx}:${state.path}`] = state.focus;
    try {
      localStorage.setItem("mp.cursor", JSON.stringify(state.cursorMemory));
    } catch (_) {}
  }
  function loadCursor() {
    return state.cursorMemory[`${state.mountIdx}:${state.path}`] || 0;
  }

  function setActiveCol(col) {
    state.activeCol = col;
    el.colMounts.dataset.active = col === "mounts" ? "true" : "false";
    el.colFiles.dataset.active = col === "files" ? "true" : "false";
    el.grid.dataset.active = col;
    // move DOM focus onto the active list so the browser's focus ring and
    // screen readers track it, while keyboard events still go to the
    // document-level handler (the list is just a focusable target).
    const target = col === "mounts" ? el.mountList : el.fileList;
    target.focus({ preventScroll: true });
  }

  function starKey(e) {
    return `${state.mountIdx}:${e.rel_path}`;
  }

  async function loadStars() {
    try {
      const refs = await api.stars();
      state.stars = new Set(refs.map((r) => `${r.mount}:${r.path}`));
    } catch (_) {
      state.stars = new Set();
    }
  }

  async function toggleStar() {
    const e = currentEntry();
    if (!e) return;
    const key = starKey(e);
    try {
      const res = await api.toggleStar(state.mountIdx, e.rel_path);
      if (res.starred) state.stars.add(key);
      else state.stars.delete(key);
      renderFiles();
    } catch (err) {
      status("star failed: " + err.message, "err");
    }
  }

  async function loadMounts() {
    try {
      state.mounts = await api.mounts();
    } catch (e) {
      status("mount load failed: " + e.message, "err");
      return;
    }
    renderMounts();
    if (state.mounts.length) selectMount(0);
    else status("no mounts configured — edit config.json", "err");
    setActiveCol("files");
  }

  function renderMounts() {
    el.mountList.innerHTML = "";
    state.mounts.forEach((m, i) => {
      const li = document.createElement("li");
      li.dataset.active = i === state.mountIdx ? "true" : "false";
      li.innerHTML = `<span class="k">${i === 9 ? "0" : i + 1}</span><span class="label">${escape(m.name)}</span>`;
      li.addEventListener("click", () => selectMount(i));
      el.mountList.appendChild(li);
    });
  }

  async function selectMount(i) {
    if (i < 0 || i >= state.mounts.length) return;
    state.mountIdx = i;
    state.path = "";
    clearFilter();
    renderMounts();
    await loadDir();
  }

  async function loadDir(presetFocus) {
    try {
      const r = await api.browse(state.mountIdx, state.path, state.sort);
      state.entries = r.entries || [];
    } catch (e) {
      status(e.message, "err");
      state.entries = [];
    }
    applyFilter();
    state.focus = presetFocus != null ? presetFocus : loadCursor();
    if (state.focus >= state.filtered.length)
      state.focus = Math.max(0, state.filtered.length - 1);
    renderFiles();
    renderCrumbs();
    updatePreview();
    // Not awaited: a different directory may sit on a different filesystem, but
    // the number is a sidebar detail — the listing must not wait on statfs.
    refreshDisk();
  }

  function applyFilter() {
    const q = state.filter.toLowerCase();
    state.filtered = !q
      ? state.entries
      : state.entries.filter((e) => e.name.toLowerCase().includes(q));
  }

  function renderCrumbs() {
    const mount = state.mounts[state.mountIdx];
    if (!mount) {
      el.crumbs.textContent = "";
      return;
    }
    const parts = state.path.split("/").filter(Boolean);
    const frag = [];
    frag.push(`<a data-i="-1">${escape(mount.name)}</a>`);
    let acc = "";
    parts.forEach((p, i) => {
      acc = acc ? acc + "/" + p : p;
      frag.push('<span class="sep">/</span>');
      if (i === parts.length - 1)
        frag.push(`<span class="cur">${escape(p)}</span>`);
      else
        frag.push(
          `<a data-i="${i}" data-path="${escape(acc)}">${escape(p)}</a>`,
        );
    });
    el.crumbs.innerHTML = frag.join("");
    el.crumbs.querySelectorAll("a").forEach((a) => {
      a.addEventListener("click", () => {
        const p = a.dataset.path || "";
        state.path = p;
        clearFilter();
        loadDir();
      });
    });
  }

  const NERD_ICONS = { folder: "󰉋", video: "", other: "󱀶", disk: "\u{f0a0}" }; // disk = nf-fa-hdd_o
  const FALLBACK_ICONS = { folder: "📁", video: "🎬", other: "📄", disk: "💾" };
  let ICONS = FALLBACK_ICONS;
  ICONS = navigator.platform.includes("Linux x86_64")
    ? NERD_ICONS
    : FALLBACK_ICONS;

  // Watch progress saved by the player page (same localStorage map).
  function loadResumeMap() {
    try {
      return JSON.parse(localStorage.getItem("mp.resume") || "{}");
    } catch (_) {
      return {};
    }
  }

  function renderFiles() {
    const resume = loadResumeMap();
    el.fileList.innerHTML = "";
    state.filtered.forEach((e, i) => {
      const li = document.createElement("li");
      li.className = e.is_dir ? "dir" : e.kind;
      li.dataset.focus = i === state.focus ? "true" : "false";
      const icon = e.is_dir
        ? ICONS.folder
        : e.kind === "video"
          ? ICONS.video
          : ICONS.other;
      let meta = e.is_dir ? "" : `${fmtSize(e.size)} · ${fmtDate(e.ctime)}`;
      const r = resume[`${state.mountIdx}:${e.rel_path}`];
      if (r && r.dur) {
        const pct = Math.min(99, Math.round((r.t / r.dur) * 100));
        meta = `<span class="resume">▍ ${pct}%</span>` + meta;
      }
      if (state.stars.has(starKey(e))) {
        meta = `<span class="star">★</span>` + meta;
      }
      li.innerHTML = `<span class="ic">${icon}</span><span class="name">${escape(e.name)}</span><span class="meta">${meta}</span>`;
      li.addEventListener("click", () => {
        state.focus = i;
        saveCursor();
        renderFiles();
        updatePreview();
        setActiveCol("files");
        if (e.is_dir) enterFocused();
      });
      li.addEventListener("dblclick", () => {
        state.focus = i;
        enterFocused();
      });
      el.fileList.appendChild(li);
    });
    const active = el.fileList.querySelector('li[data-focus="true"]');
    if (active) active.scrollIntoView({ block: "nearest" });
  }

  function currentEntry() {
    return state.filtered[state.focus];
  }

  function updatePreview() {
    const e = currentEntry();
    const reqId = ++state.previewReq;
    if (!e || e.is_dir || e.kind !== "video") {
      el.previewImg.removeAttribute("src");
      el.previewMeta.innerHTML = e
        ? `<div class="row"><span class="k">name</span><span>${escape(e.name)}</span></div>`
        : "";
      return;
    }
    const url = api.previewURL(state.mountIdx, e.rel_path, 600);
    const img = new Image();
    img.onload = () => {
      if (reqId !== state.previewReq) return;
      el.previewImg.src = url;
    };
    img.onerror = () => {
      if (reqId !== state.previewReq) return;
      el.previewImg.removeAttribute("src");
    };
    img.src = url;
    el.previewMeta.innerHTML = [
      row("name", e.name),
      row("size", fmtSize(e.size)),
      row("created", fmtDate(e.ctime)),
      row("modified", fmtDate(e.mtime)),
    ].join("");
  }

  function row(k, v) {
    return `<div class="row"><span class="k">${escape(k)}</span><span>${escape(String(v))}</span></div>`;
  }
  function escape(s) {
    return String(s).replace(
      /[&<>"']/g,
      (c) =>
        ({
          "&": "&amp;",
          "<": "&lt;",
          ">": "&gt;",
          '"': "&quot;",
          "'": "&#39;",
        })[c],
    );
  }

  function moveFiles(delta) {
    if (!state.filtered.length) return;
    state.focus = Math.max(
      0,
      Math.min(state.filtered.length - 1, state.focus + delta),
    );
    saveCursor();
    renderFiles();
    updatePreview();
  }
  function moveMounts(delta) {
    if (!state.mounts.length) return;
    const next = Math.max(
      0,
      Math.min(state.mounts.length - 1, state.mountIdx + delta),
    );
    if (next !== state.mountIdx) selectMount(next);
  }

  function enterFocused() {
    const e = currentEntry();
    if (!e) return;
    if (e.is_dir) {
      saveCursor();
      state.path = e.rel_path;
      clearFilter();
      loadDir(0);
    } else {
      openFocused();
    }
  }

  function openFocused() {
    openEntry(currentEntry());
  }

  // startSec > 0 becomes the player's `t` param — the thumbnail sheet passes
  // the clicked frame's timestamp so playback starts there instead of at the
  // stored resume position.
  function openEntry(e, startSec) {
    if (!e || e.is_dir) return;
    if (e.kind !== "video") {
      status("not a video", "err");
      return;
    }
    const q = new URLSearchParams({
      mount: String(state.mountIdx),
      path: e.rel_path,
    });
    if (startSec > 0) q.set("t", String(Math.floor(startSec)));
    location.href = "/player?" + q;
  }

  // Park the cursor on a named entry after a load that changed the listing —
  // the index isn't known until the load lands, so this runs after loadDir.
  function focusByName(name) {
    const idx = state.filtered.findIndex((e) => e.name === name);
    if (idx < 0) return;
    state.focus = idx;
    saveCursor();
    renderFiles();
    updatePreview();
  }

  function goUp() {
    if (!state.path) return;
    const childName = state.path.split("/").pop();
    saveCursor();
    state.path = state.path.split("/").slice(0, -1).join("/");
    clearFilter();
    loadDir().then(() => focusByName(childName));
  }

  // ---------- Thumbnail sheet (`p`) ----------
  // The geometry lives in the CSS (:root --sheet-* in browser.css) and is read
  // back here, so the box this fit calculation targets and the box the browser
  // actually paints can't drift apart.
  function sheetMetric(name) {
    return parseFloat(
      getComputedStyle(document.documentElement).getPropertyValue(name),
    );
  }

  // Pick the tile size that fills the most of the 80% box while still fitting
  // every frame — that's what keeps the sheet scroll-free at any window size or
  // frame count, instead of leaving it to CSS wrapping.
  function layoutSheet() {
    const shots = state.sheet;
    if (!shots || !shots.length) return;
    const gap = sheetMetric("--sheet-gap"),
      frac = sheetMetric("--sheet-fraction");
    const chrome =
      2 * (sheetMetric("--sheet-pad") + sheetMetric("--sheet-border"));
    const boxW = window.innerWidth * frac - chrome;
    const boxH =
      window.innerHeight * frac - chrome - sheetMetric("--sheet-head-h");
    const aspect = (shots[0].w || 16) / (shots[0].h || 9);
    let best = { cols: 1, w: 0 };
    for (let c = 1; c <= shots.length; c++) {
      const rows = Math.ceil(shots.length / c);
      const w = Math.min(
        (boxW - gap * (c - 1)) / c,
        ((boxH - gap * (rows - 1)) / rows) * aspect,
      );
      if (w > best.w) best = { cols: c, w };
    }
    // Never upscale past the frame's own 300px: with only a few frames the fit
    // box is far bigger than the image, and a blurry sheet in a slightly
    // smaller card beats a sharp one nobody asked to be stretched.
    const tw = Math.max(1, Math.floor(Math.min(best.w, shots[0].w || best.w)));
    el.sheetGrid.style.gridTemplateColumns = `repeat(${best.cols}, ${tw}px)`;
    el.sheetGrid.style.gridAutoRows =
      Math.max(1, Math.floor(tw / aspect)) + "px";
  }

  // A drag-resize fires resize ~60×/s and each layout re-scales up to 60
  // images; coalesce to one per frame.
  let sheetResizePending = false;
  function onSheetResize() {
    if (sheetResizePending) return;
    sheetResizePending = true;
    requestAnimationFrame(() => {
      sheetResizePending = false;
      layoutSheet();
    });
  }

  async function openSheet() {
    const e = currentEntry();
    if (!e || e.is_dir || e.kind !== "video") {
      status("not a video", "err");
      return;
    }
    if (state.sheetBusy) return;
    state.sheetBusy = true;
    state.sheetAbort = new AbortController();
    status(`thumbnails: rendering ${e.name}…`);
    let res;
    try {
      res = await api.sheet(
        state.mountIdx,
        e.rel_path,
        state.sheetAbort.signal,
      );
    } catch (err) {
      // An abort is the user changing their mind, not a failure worth a
      // red status line.
      if (err.name !== "AbortError")
        status("thumbnails failed: " + err.message, "err");
      return;
    } finally {
      state.sheetBusy = false;
      state.sheetAbort = null;
    }
    const shots = (res && res.shots) || [];
    if (!shots.length) {
      status("no frames rendered", "err");
      return;
    }
    state.sheet = shots;
    // The sheet outlives the cursor: clicking a frame navigates using the entry
    // the frames were rendered from, not whatever is focused by then.
    state.sheetEntry = e;
    // The spacing is the server's constant, so read it back rather than
    // restating "10 min" here and letting the two drift.
    const every = res.interval
      ? ` · every ${Math.round(res.interval / 60)} min`
      : "";
    el.sheetHead.innerHTML =
      `${escape(e.name)} <span class="dim">· ${shots.length} frame${shots.length > 1 ? "s" : ""}${every}` +
      `${res.truncated ? ` · capped at ${shots.length}` : ""}</span>`;
    // Buttons, not divs: each frame is a link into the video at its own
    // timestamp, so it should be reachable by Tab and activate on Enter too.
    el.sheetGrid.innerHTML = shots
      .map(
        (s) =>
          `<button type="button" class="sheet-shot" data-t="${s.t}" title="play from ${fmtTime(s.t)}">` +
          `<img src="${s.data}" alt="" /><span class="t">${fmtTime(s.t)}</span></button>`,
      )
      .join("");
    layoutSheet();
    el.sheet.hidden = false;
    // Park focus on the first frame: Tab order otherwise starts at the page
    // chrome behind the overlay, so the frames would be several tabs away from
    // a keyboard that just pressed `p`. closeSheet hands focus back to the list.
    const first = el.sheetGrid.querySelector(".sheet-shot");
    if (first) first.focus({ preventScroll: true });
    window.addEventListener("resize", onSheetResize);
    status(`${shots.length} frames`, "ok");
  }

  function closeSheet() {
    if (el.sheet.hidden) return;
    el.sheet.hidden = true;
    el.sheetGrid.innerHTML = ""; // the frames are megabytes of data: URIs
    state.sheet = null;
    state.sheetEntry = null;
    window.removeEventListener("resize", onSheetResize);
    setActiveCol(state.activeCol);
  }

  // A frame is a seek target: open the video it was rendered from at that
  // timestamp. The overlay is left standing — the page is navigating away.
  function playSheetShot(shot) {
    const t = parseFloat(shot.dataset.t);
    if (!isFinite(t) || !state.sheetEntry) return;
    openEntry(state.sheetEntry, t);
  }

  // One handler for mouse and touch: a frame plays from its timestamp, the
  // backdrop closes, and the card's own chrome (title, gaps) does neither —
  // a stray click while reading the header shouldn't dismiss the sheet.
  function onSheetPointer(ev) {
    const shot = ev.target.closest(".sheet-shot");
    if (shot) {
      ev.preventDefault();
      playSheetShot(shot);
      return;
    }
    if (ev.target.closest(".sheet-card")) return;
    ev.preventDefault();
    closeSheet();
  }

  // Abandoning a render kills the ffmpegthumbnailers behind it — a 10h file is
  // 15 waves of work nobody is waiting for any more.
  function cancelSheet() {
    if (state.sheetAbort) {
      state.sheetAbort.abort();
      status("thumbnails cancelled");
    }
  }

  // ---------- Disk used space ----------
  const DISK_POLL_MS = 60000;

  async function refreshDisk() {
    // A backgrounded tab's number is read by nobody; visibilitychange below
    // refreshes it the moment the tab comes back, so it's never stale on screen.
    if (document.hidden) return;
    let d;
    try {
      // Report on the filesystem of the directory in view; before the mount
      // list has loaded there is none, and the server falls back to the
      // configured disk.
      const haveDir = state.mounts.length > 0;
      d = await api.disk(
        haveDir ? state.mountIdx : null,
        haveDir ? state.path : null,
      );
    } catch (_) {
      d = null;
    }
    // No disk configured, device not mounted, non-Linux build: stay hidden
    // rather than render a meaningless 0%.
    if (!d || !d.ok || !d.total_bytes) {
      el.disk.hidden = true;
      return;
    }
    const pct = Math.max(0, Math.min(100, d.percent_used));
    el.diskFill.style.width = pct + "%";
    el.diskPct.textContent = Math.round(pct) + "%";
    el.disk.dataset.level = pct > 90 ? "crit" : pct > 80 ? "warn" : "ok";
    el.disk.title =
      `${fmtSize(d.used_bytes)} used of ${fmtSize(d.total_bytes)}` +
      ` — ${fmtSize(d.avail_bytes)} free` +
      (d.mountpoint ? ` on ${d.mountpoint}` : "");
    el.disk.hidden = false;
  }

  // ---------- Modals ----------
  function modal({
    title,
    bodyHTML,
    ok = "OK",
    cancel = "Cancel",
    onOk,
    onCancel,
  }) {
    el.modalTitle.textContent = title;
    el.modalBody.innerHTML = bodyHTML;
    el.modalOk.textContent = ok;
    el.modalCancel.textContent = cancel;
    el.modal.hidden = false;
    const close = () => {
      el.modal.hidden = true;
      el.modalOk.onclick = null;
      el.modalCancel.onclick = null;
      setActiveCol(state.activeCol); // restore focus to active list
    };
    el.modalOk.onclick = async () => {
      try {
        await (onOk && onOk());
        close();
      } catch (e) {
        status(e.message, "err");
      }
    };
    el.modalCancel.onclick = () => {
      onCancel && onCancel();
      close();
    };
    const firstInput = el.modalBody.querySelector("input,select,button");
    if (firstInput) firstInput.focus();
  }

  function askRename() {
    const e = currentEntry();
    if (!e) return;
    modal({
      title: "Rename",
      bodyHTML: `<input id="_new" type="text" value="${escape(e.name)}" />`,
      ok: "Rename",
      onOk: async () => {
        const v = document.getElementById("_new").value.trim();
        if (!v) return;
        await api.rename(state.mountIdx, e.rel_path, v);
        await loadDir();
        status("renamed", "ok");
      },
    });
    setTimeout(() => {
      const i = document.getElementById("_new");
      if (i) {
        i.focus();
        const dot = i.value.lastIndexOf(".");
        i.setSelectionRange(0, dot > 0 ? dot : i.value.length);
      }
    }, 0);
  }

  function askDelete() {
    const e = currentEntry();
    if (!e) return;
    modal({
      title: "Delete",
      bodyHTML: `<div>Delete <strong>${escape(e.name)}</strong>? This cannot be undone.</div>`,
      ok: "Delete",
      onOk: async () => {
        await api.del(state.mountIdx, e.rel_path);
        await loadDir();
        refreshDisk();
        status("deleted", "ok");
      },
    });
  }

  // Drop the trailing ".part" an interrupted download/recording leaves on a
  // finished "….mp4.part" file. No confirm modal — it's one rename, and the
  // dialog would cost more than the keystroke saves.
  const PART_TAIL = /\.mp4\.part$/i;
  async function stripPart() {
    const e = currentEntry();
    if (!e || e.is_dir || !PART_TAIL.test(e.name)) {
      status("not a .mp4.part file", "err");
      return;
    }
    const newName = e.name.slice(0, -".part".length);
    // os.Rename would silently clobber an existing target, so refuse when the
    // stripped name is already taken (state.entries is the unfiltered listing).
    if (state.entries.some((x) => x.name === newName)) {
      status(`${newName} already exists`, "err");
      return;
    }
    try {
      await api.rename(state.mountIdx, e.rel_path, newName);
    } catch (err) {
      status("rename failed: " + err.message, "err");
      return;
    }
    await loadDir();
    focusByName(newName);
    status("→ " + newName, "ok");
  }

  function askSort() {
    const opts = [
      ["ctime_desc", "Created (new → old)"],
      ["ctime_asc", "Created (old → new)"],
      ["name_asc", "Name (A → Z)"],
      ["name_desc", "Name (Z → A)"],
      ["size_desc", "Size (large → small)"],
      ["size_asc", "Size (small → large)"],
    ];
    const html =
      "<ul id='_sort'>" +
      opts
        .map(
          ([k, v]) =>
            `<li data-k="${k}" data-selected="${state.sort === k ? "true" : "false"}">${escape(v)}</li>`,
        )
        .join("") +
      "</ul>";
    modal({
      title: "Sort",
      bodyHTML: html,
      ok: "Apply",
      onOk: async () => {
        const sel = document.querySelector('#_sort li[data-selected="true"]');
        if (sel) {
          state.sort = sel.dataset.k;
          try {
            localStorage.setItem("mp.sort", state.sort);
          } catch (_) {}
          await loadDir();
        }
      },
    });
    document.querySelectorAll("#_sort li").forEach((li) => {
      li.addEventListener("click", () => {
        document
          .querySelectorAll("#_sort li")
          .forEach((x) => (x.dataset.selected = "false"));
        li.dataset.selected = "true";
      });
    });
  }

  function openFilter() {
    state.focusBeforeFilter = state.focus;
    el.filter.hidden = false;
    el.filterInput.value = state.filter;
    el.filterInput.focus();
    el.filterInput.select();
  }
  function closeFilter(commit) {
    el.filter.hidden = true;
    if (!commit) {
      // Restore the pre-filter focus so ESC feels like a true cancel.
      state.filter = "";
      applyFilter();
      state.focus = Math.min(
        state.focusBeforeFilter || 0,
        Math.max(0, state.filtered.length - 1),
      );
      renderFiles();
      updatePreview();
    }
    el.filterInput.blur();
    setActiveCol("files");
  }
  function toggleFilter() {
    if (el.filter.hidden) openFilter();
    else closeFilter(false);
  }
  // Drop the active filter when navigating to a different directory so the
  // new listing starts unfiltered. Same-dir reloads (rename/delete/sort) keep it.
  function clearFilter() {
    state.filter = "";
    if (!el.filter.hidden) el.filter.hidden = true;
    el.filterInput.value = "";
  }

  // ---------- Keybinds ----------
  document.addEventListener("keydown", (ev) => {
    // The open sheet owns the keyboard. Escape closes it; Tab and Enter/Space
    // still work so the frames can be reached and played without a mouse; every
    // other key is swallowed rather than acting on the file list behind the
    // overlay (`G` would otherwise jump the cursor you can't see).
    if (!el.sheet.hidden) {
      if (ev.key === "Escape") {
        ev.preventDefault();
        closeSheet();
        return;
      }
      const onShot =
        ev.target instanceof Element && ev.target.closest(".sheet-shot");
      if (ev.key === "Tab") {
        // Keep Tab inside the overlay — wrapping at the ends beats tabbing off
        // into the file list nobody can see.
        const shots = Array.from(el.sheetGrid.querySelectorAll(".sheet-shot"));
        if (!shots.length) return;
        const i = shots.indexOf(onShot);
        const next = ev.shiftKey ? i - 1 : i + 1;
        if (i < 0 || next < 0 || next >= shots.length) {
          ev.preventDefault();
          shots[ev.shiftKey ? shots.length - 1 : 0].focus();
        }
        return;
      }
      if (onShot && (ev.key === "Enter" || ev.key === " ")) {
        // Activate it here rather than letting the button's own click fire:
        // the overlay listens on mousedown, so a synthesized click would be a
        // second, differently-wired path to the same navigation.
        ev.preventDefault();
        playSheetShot(onShot);
        return;
      }
      ev.preventDefault();
      return;
    }
    // While it's still rendering there's nothing to dismiss, but Escape should
    // still call it off; every other key keeps working on the list.
    if (state.sheetBusy && ev.key === "Escape") {
      ev.preventDefault();
      cancelSheet();
      return;
    }
    if (!el.modal.hidden) {
      if (ev.key === "Escape") el.modalCancel.click();
      else if (ev.key === "Enter" && ev.target.tagName !== "BUTTON")
        el.modalOk.click();
      else if (
        ev.key === "y" &&
        ev.target.tagName !== "BUTTON" &&
        ev.target.tagName !== "INPUT" &&
        ev.target.tagName !== "TEXTAREA"
      )
        el.modalOk.click();
      return;
    }
    if (document.activeElement === el.filterInput) {
      if (ev.key === "Escape") {
        ev.preventDefault();
        closeFilter(false);
      } else if (ev.key === "Enter") {
        ev.preventDefault();
        state.filter = el.filterInput.value;
        applyFilter();
        state.focus = 0;
        renderFiles();
        updatePreview();
        closeFilter(true);
      }
      return;
    }

    // Tab switches active column
    if (ev.key === "Tab") {
      ev.preventDefault();
      setActiveCol(state.activeCol === "files" ? "mounts" : "files");
      return;
    }

    // mount jump: 1..9,0 (always global — spec says these jump regardless of column)
    if (/^[0-9]$/.test(ev.key)) {
      const idx = ev.key === "0" ? 9 : parseInt(ev.key, 10) - 1;
      if (idx < state.mounts.length) {
        selectMount(idx);
        ev.preventDefault();
      }
      return;
    }

    if (state.activeCol === "mounts") {
      switch (ev.key) {
        case "j":
        case "ArrowDown":
          moveMounts(1);
          ev.preventDefault();
          break;
        case "k":
        case "ArrowUp":
          moveMounts(-1);
          ev.preventDefault();
          break;
        case "l":
        case "ArrowRight":
        case "Enter":
          setActiveCol("files");
          ev.preventDefault();
          break;
      }
      return;
    }

    // activeCol === "files"
    switch (ev.key) {
      case "j":
      case "ArrowDown":
        moveFiles(1);
        ev.preventDefault();
        break;
      case "k":
      case "ArrowUp":
        moveFiles(-1);
        ev.preventDefault();
        break;
      case "h":
      case "ArrowLeft":
        goUp();
        ev.preventDefault();
        break;
      case "l":
      case "ArrowRight":
      case "Enter":
        enterFocused();
        ev.preventDefault();
        break;
      case "g":
        if (state.gState) {
          state.gState = 0;
          state.focus = 0;
          renderFiles();
          updatePreview();
          saveCursor();
        } else {
          state.gState = 1;
          setTimeout(() => (state.gState = 0), 500);
        }
        ev.preventDefault();
        break;
      case "G":
        state.focus = Math.max(0, state.filtered.length - 1);
        renderFiles();
        updatePreview();
        saveCursor();
        ev.preventDefault();
        break;
      case "r":
        askRename();
        ev.preventDefault();
        break;
      case "d":
        askDelete();
        ev.preventDefault();
        break;
      case "y":
        toggleStar();
        ev.preventDefault();
        break;
      // Backspace, not "q": the player page owns "q" as close-and-return, and a
      // key that means two different things on the two pages is a trap.
      case "Backspace":
        stripPart();
        ev.preventDefault();
        break;
      case "p":
        openSheet();
        ev.preventDefault();
        break;
      case "f":
      case "/":
        toggleFilter();
        ev.preventDefault();
        break;
      case "o":
        askSort();
        ev.preventDefault();
        break;
      // Direct sort keybinds — no dialog. Capital letter = ascending.
      case "m":
        applySort("ctime_desc");
        ev.preventDefault();
        break;
      case "M":
        applySort("ctime_asc");
        ev.preventDefault();
        break;
      case "s":
        applySort("size_desc");
        ev.preventDefault();
        break;
      case "S":
        applySort("size_asc");
        ev.preventDefault();
        break;
      case "n":
        applySort("name_asc");
        ev.preventDefault();
        break;
      case "N":
        applySort("name_desc");
        ev.preventDefault();
        break;
    }
  });

  async function applySort(key) {
    state.sort = key;
    try {
      localStorage.setItem("mp.sort", key);
    } catch (_) {}
    status("sort: " + key.replace("_", " "), "ok");
    await loadDir();
  }

  // The sheet overlay covers the viewport, so one listener on it catches every
  // press — on a frame, on the card, or on the backdrop. mousedown (not click)
  // so the press itself acts; touchstart is deliberately not passive, since
  // both branches preventDefault to stop a ghost click landing behind.
  el.sheet.addEventListener("mousedown", onSheetPointer);
  el.sheet.addEventListener("touchstart", onSheetPointer);

  // Clicking anywhere in the mount list activates that column
  el.mountList.addEventListener("mousedown", () => setActiveCol("mounts"));
  el.fileList.addEventListener("mousedown", () => setActiveCol("files"));

  // Filter live update; blur also closes the filter (click outside)
  el.filterInput.addEventListener("input", () => {
    state.filter = el.filterInput.value;
    applyFilter();
    state.focus = 0;
    renderFiles();
    updatePreview();
  });
  el.filterInput.addEventListener("blur", () => {
    // If user clicked elsewhere without pressing ESC/Enter, treat as commit
    // if there's a filter value, otherwise cancel-restore.
    if (!el.filter.hidden) {
      if (state.filter) closeFilter(true);
      else closeFilter(false);
    }
  });

  // mobile nav — always rendered; collapsible via toggle, persisted per session
  const MOBILE_NAV_KEY = "mp.mobilenav.open";
  function setMobileNavOpen(open) {
    el.mobileNav.hidden = !open;
    el.mobileNavToggle.setAttribute("aria-expanded", open ? "true" : "false");
    try {
      sessionStorage.setItem(MOBILE_NAV_KEY, open ? "1" : "0");
    } catch (_) {}
  }
  setMobileNavOpen(sessionStorage.getItem(MOBILE_NAV_KEY) === "1");
  el.mobileNavToggle.addEventListener("click", () => {
    setMobileNavOpen(el.mobileNav.hidden);
  });

  el.mobileNav.addEventListener("click", (ev) => {
    const b = ev.target.closest("button");
    if (!b) return;
    switch (b.dataset.act) {
      case "up":
        state.activeCol === "mounts" ? moveMounts(-1) : moveFiles(-1);
        break;
      case "down":
        state.activeCol === "mounts" ? moveMounts(1) : moveFiles(1);
        break;
      case "enter":
        state.activeCol === "mounts" ? setActiveCol("files") : enterFocused();
        break;
      case "sort":
        askSort();
        break;
      case "filter":
        toggleFilter();
        break;
      case "rename":
        askRename();
        break;
      case "unpart":
        stripPart();
        break;
      case "sheet":
        openSheet();
        break;
      case "delete":
        askDelete();
        break;
    }
  });

  loadStars().then(loadMounts);
  el.diskIc.textContent = ICONS.disk; // never changes — not per poll
  refreshDisk();
  setInterval(refreshDisk, DISK_POLL_MS);
  document.addEventListener("visibilitychange", () => {
    if (!document.hidden) refreshDisk();
  });
})();
