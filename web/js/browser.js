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
    // cursorMemory[`${mountIdx}:${path}`] = focus index
    cursorMemory: JSON.parse(localStorage.getItem("mp.cursor") || "{}"),
    previewReq: 0,
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
        loadDir();
      });
    });
  }

  const NERD_ICONS = { folder: "󰉋", video: "", other: "󱀶" };
  const FALLBACK_ICONS = { folder: "📁", video: "🎬", other: "📄" };
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
      loadDir(0);
    } else {
      openFocused();
    }
  }

  function openFocused() {
    const e = currentEntry();
    if (!e || e.is_dir) return;
    if (e.kind !== "video") {
      status("not a video", "err");
      return;
    }
    const q = new URLSearchParams({
      mount: String(state.mountIdx),
      path: e.rel_path,
    });
    location.href = "/player?" + q;
  }

  function goUp() {
    if (!state.path) return;
    const childName = state.path.split("/").pop();
    saveCursor();
    state.path = state.path.split("/").slice(0, -1).join("/");
    loadDir().then(() => {
      const idx = state.filtered.findIndex((e) => e.name === childName);
      if (idx >= 0) {
        state.focus = idx;
        saveCursor();
        renderFiles();
        updatePreview();
      }
    });
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
        status("deleted", "ok");
      },
    });
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

  // ---------- Keybinds ----------
  document.addEventListener("keydown", (ev) => {
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
      case "Backspace":
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
      case "delete":
        askDelete();
        break;
    }
  });

  loadMounts();
})();
