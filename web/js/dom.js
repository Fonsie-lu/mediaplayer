// Shared substrate for the browser page's modules: the one mutable `state`
// object, the resolved DOM handles, and the few helpers that touch nothing but
// those two.
//
// Everything here is imported by more than one module. `clearFilter` in
// particular lives here rather than with the rest of the filter UI precisely
// because it only pokes state and el — keeping it next to openFilter/closeFilter
// would make listing.js and dialogs.js import each other.
// Part of the file-browser page. api.js is loaded as a classic script before
// these modules, so window.api / fmtSize / fmtDate / fmtTime are globals here.

// Web storage throws in private-mode Safari, when the quota is full, on a
// corrupt value, and — with site data blocked — on merely touching the
// `localStorage` global; nothing the page keeps there is worth failing an
// interaction over, let alone throwing during module evaluation. Every read
// and write of mp.* goes through these three. `session` picks sessionStorage
// over localStorage; the storage object is looked up inside the try, since
// that lookup is what throws when site data is blocked.
function backing(session) {
  return session ? window.sessionStorage : window.localStorage;
}
export function store(key, value, session = false) {
  try {
    backing(session).setItem(key, value);
  } catch (_) {}
}
export function load(key, session = false) {
  try {
    return backing(session).getItem(key);
  } catch (_) {
    return null;
  }
}
export function loadJSON(key, session = false) {
  try {
    return JSON.parse(load(key, session) || "{}") || {};
  } catch (_) {
    return {};
  }
}

export const state = {
  mounts: [],
  mountIdx: 0,
  path: "",
  entries: [],
  filtered: [],
  focus: 0,
  sort: load("mp.sort") || "ctime_desc",
  filter: "",
  stars: new Set(), // keys: `${mountIdx}:${rel_path}`
  // cursorMemory[`${mountIdx}:${path}`] = focus index
  cursorMemory: loadJSON("mp.cursor"),
  previewReq: 0,
  sheet: null, // frames of the open thumbnail sheet, null when closed
  sheetEntry: null, // the entry those frames came from (click target)
  sheetBusy: false, // a /api/sheet render is in flight
  gState: 0, // for "gg" double-press
  activeCol: "files", // "files" | "mounts" — which column Tab is on
  focusBeforeFilter: 0, // restore this on filter ESC…
  filterBeforeOpen: "", // …together with the filter it indexed into
  sheetAbort: null, // AbortController of the in-flight /api/sheet render
};

export const el = {
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

export function status(msg, kind) {
  el.status.textContent = msg || "";
  el.status.className = "statusbar" + (kind ? " " + kind : "");
}

export function saveCursor() {
  state.cursorMemory[`${state.mountIdx}:${state.path}`] = state.focus;
  store("mp.cursor", JSON.stringify(state.cursorMemory));
}
export function loadCursor() {
  return state.cursorMemory[`${state.mountIdx}:${state.path}`] || 0;
}

export function setActiveCol(col) {
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

const NERD_ICONS = {
  folder: "󰉋",
  video: "",
  other: "󱀶",
  disk: "\u{f0a0}",
}; // disk = nf-fa-hdd_o
const FALLBACK_ICONS = {
  folder: "📁",
  video: "🎬",
  other: "📄",
  disk: "💾",
};
export const ICONS = navigator.platform.includes("Linux x86_64")
  ? NERD_ICONS
  : FALLBACK_ICONS;

const ESCAPES = {
  "&": "&amp;",
  "<": "&lt;",
  ">": "&gt;",
  '"': "&quot;",
  "'": "&#39;",
};
export function escape(s) {
  return String(s).replace(/[&<>"']/g, (c) => ESCAPES[c]);
}

// Drop the active filter when navigating to a different directory so the
// new listing starts unfiltered. Same-dir reloads (rename/delete/sort) keep it.
export function clearFilter() {
  state.filter = "";
  if (!el.filter.hidden) el.filter.hidden = true;
  el.filterInput.value = "";
}
