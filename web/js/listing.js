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
  loadJSON,
  setActiveCol,
  clearFilter,
} from "./dom.js";
import { refreshDisk } from "./disk.js";

function starKey(e) {
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
    refreshList(state.focus);
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

// Rows carry their index and nothing else: clicks are handled by one
// delegated listener in browser.js, so a re-render doesn't churn listeners.
function renderMounts() {
  el.mountList.innerHTML = state.mounts
    .map(
      (m, i) =>
        `<li data-i="${i}" data-active="${i === state.mountIdx}">` +
        `<span class="k">${i === 9 ? "0" : i + 1}</span>` +
        `<span class="label">${escape(m.name)}</span></li>`,
    )
    .join("");
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
  dirPreviewCache.clear();
  try {
    const r = await api.browse(state.mountIdx, state.path, state.sort);
    state.entries = r.entries || [];
  } catch (e) {
    status(e.message, "err");
    state.entries = [];
  }
  applyFilter();
  refreshList(presetFocus != null ? presetFocus : loadCursor());
  renderCrumbs();
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
      frag.push(`<a data-i="${i}" data-path="${escape(acc)}">${escape(p)}</a>`);
  });
  el.crumbs.innerHTML = frag.join("");
}

// openCrumb is the delegated crumb click (wired in browser.js): the root crumb
// carries no data-path, which is how it navigates to the mount root.
export function openCrumb(path) {
  state.path = path || "";
  clearFilter();
  loadDir();
}

// Watch progress saved by the player page (same localStorage map). Read per
// rebuild, which is per directory load / filter / sort / star — not per cursor
// key, since those only repaint two rows now.
function loadResumeMap() {
  return loadJSON("mp.resume");
}

// renderFiles rebuilds the whole list, so it is for content changes (a new
// directory, a filter, a sort, a star toggle) — moving the cursor goes through
// setFocus, which repaints two rows instead of hundreds. Rows are plain markup
// carrying their index; browser.js has the one delegated click listener.
function renderFiles() {
  const resume = loadResumeMap();
  el.fileList.innerHTML = state.filtered
    .map((e, i) => fileRow(e, i, resume))
    .join("");
  scrollFocusIntoView(el.fileList.children[state.focus]);
}

function fileRow(e, i, resume) {
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
  return (
    `<li class="${e.is_dir ? "dir" : e.kind}" data-i="${i}" data-focus="${i === state.focus}">` +
    `<span class="ic">${icon}</span>` +
    `<span class="name">${escape(e.name)}</span>` +
    `<span class="meta">${meta}</span></li>`
  );
}

function scrollFocusIntoView(row) {
  if (row) row.scrollIntoView({ block: "nearest" });
}

// renderFocus moves the highlight without touching the rest of the list. The
// full rebuild used to run on every j/k, which on a large directory meant
// discarding and recreating every row (and its listeners) per keystroke.
// Both rows are indexed directly — searching for [data-focus="true"] would be
// a document-order scan of the whole list for something already known.
function renderFocus(fromIndex) {
  const prev = el.fileList.children[fromIndex];
  if (prev) prev.dataset.focus = "false";
  const next = el.fileList.children[state.focus];
  if (next) next.dataset.focus = "true";
  scrollFocusIntoView(next);
}

export function currentEntry() {
  return state.filtered[state.focus];
}

// Directory listings shown in the preview pane, keyed `mount:path:sort`. The
// cursor crosses folders faster than the fetches come back, so without this a
// held `j` would re-request the same directory on every pass over it. Cleared
// by loadDir, which is also every mutation (rename, delete, sort) — so the
// cache can never outlive the listing it was read from.
const dirPreviewCache = new Map();
const dirPreviewCacheMax = 64;
// A folder with thousands of children would put megabytes of markup into a
// pane the user only glances at; the row count is reported either way.
const dirPreviewMax = 200;

function updatePreview() {
  const e = currentEntry();
  const reqId = ++state.previewReq;
  if (e && e.is_dir) {
    el.previewImg.removeAttribute("src");
    previewDir(e, reqId);
    return;
  }
  if (!e || e.kind !== "video") {
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

// previewDir fills the preview column for a focused folder: the folder's own
// meta rows plus its children, in the listing's active sort order (the server
// sorts, so passing state.sort is the whole of it). A cache hit renders in the
// same turn as the keystroke; a miss shows the meta rows immediately and fills
// the list in when the fetch lands. Every write is guarded by the reqId that
// updatePreview stamped, so a slow directory can't paint over a newer cursor
// position — the same guard the image preview uses.
function previewDir(e, reqId) {
  const key = `${state.mountIdx}:${e.rel_path}:${state.sort}`;
  const cached = dirPreviewCache.get(key);
  if (cached) {
    el.previewMeta.innerHTML = dirPreviewHTML(cached);
    return;
  }
  el.previewMeta.innerHTML = dirPreviewHTML(null);
  api
    .browse(state.mountIdx, e.rel_path, state.sort)
    .then((r) => {
      const entries = r.entries || [];
      if (dirPreviewCache.size >= dirPreviewCacheMax) {
        // Map iterates in insertion order, so this drops the oldest entry.
        dirPreviewCache.delete(dirPreviewCache.keys().next().value);
      }
      dirPreviewCache.set(key, entries);
      if (reqId === state.previewReq)
        el.previewMeta.innerHTML = dirPreviewHTML(entries);
    })
    .catch((err) => {
      if (reqId === state.previewReq)
        el.previewMeta.innerHTML = `<div class="dir-note err">${escape(err.message)}</div>`;
    });
}

// entries === null means the fetch is still out. The folder's own name is not
// repeated here — the cursor is sitting on it in the file list — so the pane is
// the child rows with the tally as a footer under them.
function dirPreviewHTML(entries) {
  if (!entries) return `<div class="dir-note">…</div>`;
  const dirs = entries.filter((c) => c.is_dir).length;
  const files = entries.length - dirs;
  const counts = [
    dirs ? `${dirs} folder${dirs === 1 ? "" : "s"}` : "",
    files ? `${files} file${files === 1 ? "" : "s"}` : "",
  ]
    .filter(Boolean)
    .join(" · ");
  const shown = entries.slice(0, dirPreviewMax);
  const rest = entries.length - shown.length;
  return (
    (shown.length
      ? `<ul class="dir-list">${shown.map(dirPreviewRow).join("")}</ul>`
      : "") +
    (rest ? `<div class="dir-note">… ${rest} more</div>` : "") +
    `<div class="row dir-total"><span class="k">contains</span><span>${escape(counts || "empty")}</span></div>`
  );
}

// Icon and filename, nothing else: the pane is a glance at what is inside the
// folder, and sizes there duplicated the file list without being readable at
// this width.
//
// Inert markup, like every other rendered list on the page — the preview is a
// read-only look ahead, so these rows carry no index and take no clicks. The
// name span is deliberately NOT class="meta"-adjacent: `.preview .meta` (the
// head rows' container) is a descendant selector, so a `meta` class in here
// inherits its `width: 100%` and squeezes the filename to zero width.
function dirPreviewRow(c) {
  const icon = c.is_dir
    ? ICONS.folder
    : c.kind === "video"
      ? ICONS.video
      : ICONS.other;
  return (
    `<li class="${c.is_dir ? "dir" : c.kind}">` +
    `<span class="ic">${icon}</span>` +
    `<span class="name">${escape(c.name)}</span></li>`
  );
}

// clamp keeps an index inside a list of n items.
const clamp = (i, n) => Math.max(0, Math.min(n - 1, i));
const clampFocus = (i) => clamp(i, state.filtered.length);

// setFocus is the one way the cursor moves: clamp, remember, repaint the
// highlight and the preview. Every caller used to repeat this trio and it was
// easy to forget saveCursor. A move that lands where the cursor already is
// does nothing — updatePreview would otherwise re-request the same thumbnail
// (a server-side ffmpegthumbnailer spawn) on every repeat of a key held at the
// end of the list.
export function setFocus(i) {
  const next = clampFocus(i);
  if (next === state.focus) return;
  const from = state.focus;
  state.focus = next;
  saveCursor();
  renderFocus(from);
  updatePreview();
}

// refreshList is setFocus's counterpart for when the listing itself changed
// (filter applied or cancelled, a star toggled, a fresh directory): rebuild the
// rows, then land the cursor. It does not persist the cursor — the listing it
// is landing in may not be the one the position was remembered for.
export function refreshList(i) {
  state.focus = clampFocus(i);
  renderFiles();
  updatePreview();
}

export function moveFiles(delta) {
  if (!state.filtered.length) return;
  setFocus(state.focus + delta);
}
export function moveMounts(delta) {
  if (!state.mounts.length) return;
  const next = clamp(state.mountIdx + delta, state.mounts.length);
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

function openFocused() {
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
function focusByName(name) {
  const idx = state.filtered.findIndex((e) => e.name === name);
  if (idx < 0) return;
  setFocus(idx);
}

export function goUp() {
  if (!state.path) return;
  const childName = state.path.split("/").pop();
  saveCursor();
  state.path = state.path.split("/").slice(0, -1).join("/");
  clearFilter();
  loadDir().then(() => focusByName(childName));
}
