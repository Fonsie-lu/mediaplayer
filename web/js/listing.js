// Mounts, directory listing, cursor movement and the preview pane — the page's
// core. Imports the substrate and the disk widget; imported in turn by the
// sheet, the dialogs and the page entry point, so it must not import those.
// Part of the file-browser page. api.js is loaded as a classic script before
// these modules, so window.api / fmtSize / fmtDate / fmtTime are globals here.

import {
  state,
  el,
  status,
  escape,
  ICONS,
  saveCursor,
  loadCursor,
  setActiveCol,
  clearFilter,
} from "./dom.js";
import { refreshDisk } from "./disk.js";

export function starKey(e) {
  return `${state.mountIdx}:${e.rel_path}`;
}

export async function loadStars() {
  try {
    const refs = await api.stars();
    state.stars = new Set(refs.map((r) => `${r.mount}:${r.path}`));
  } catch (_) {
    state.stars = new Set();
  }
}

export async function toggleStar() {
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

export async function loadMounts() {
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

export function renderMounts() {
  el.mountList.innerHTML = "";
  state.mounts.forEach((m, i) => {
    const li = document.createElement("li");
    li.dataset.active = i === state.mountIdx ? "true" : "false";
    li.innerHTML = `<span class="k">${i === 9 ? "0" : i + 1}</span><span class="label">${escape(m.name)}</span>`;
    li.addEventListener("click", () => selectMount(i));
    el.mountList.appendChild(li);
  });
}

export async function selectMount(i) {
  if (i < 0 || i >= state.mounts.length) return;
  state.mountIdx = i;
  state.path = "";
  clearFilter();
  renderMounts();
  await loadDir();
}

export async function loadDir(presetFocus) {
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

export function applyFilter() {
  const q = state.filter.toLowerCase();
  state.filtered = !q
    ? state.entries
    : state.entries.filter((e) => e.name.toLowerCase().includes(q));
}

export function renderCrumbs() {
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
      frag.push(`<a data-i="${i}" data-path="${escape(acc)}">${escape(p)}</a>`);
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

// Watch progress saved by the player page (same localStorage map).
export function loadResumeMap() {
  try {
    return JSON.parse(localStorage.getItem("mp.resume") || "{}");
  } catch (_) {
    return {};
  }
}

export function renderFiles() {
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

export function currentEntry() {
  return state.filtered[state.focus];
}

export function updatePreview() {
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

export function row(k, v) {
  return `<div class="row"><span class="k">${escape(k)}</span><span>${escape(String(v))}</span></div>`;
}

export function moveFiles(delta) {
  if (!state.filtered.length) return;
  state.focus = Math.max(
    0,
    Math.min(state.filtered.length - 1, state.focus + delta),
  );
  saveCursor();
  renderFiles();
  updatePreview();
}
export function moveMounts(delta) {
  if (!state.mounts.length) return;
  const next = Math.max(
    0,
    Math.min(state.mounts.length - 1, state.mountIdx + delta),
  );
  if (next !== state.mountIdx) selectMount(next);
}

export function enterFocused() {
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

export function openFocused() {
  openEntry(currentEntry());
}

// startSec > 0 becomes the player's `t` param — the thumbnail sheet passes
// the clicked frame's timestamp so playback starts there instead of at the
// stored resume position.
export function openEntry(e, startSec) {
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
export function focusByName(name) {
  const idx = state.filtered.findIndex((e) => e.name === name);
  if (idx < 0) return;
  state.focus = idx;
  saveCursor();
  renderFiles();
  updatePreview();
}

export function goUp() {
  if (!state.path) return;
  const childName = state.path.split("/").pop();
  saveCursor();
  state.path = state.path.split("/").slice(0, -1).join("/");
  clearFilter();
  loadDir().then(() => focusByName(childName));
}
