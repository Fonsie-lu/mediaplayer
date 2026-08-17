// The `p` thumbnail sheet: an overlay of one frame per 10 minutes of runtime,
// each frame a seek target.
// Part of the file-browser page. api.js is loaded as a classic script before
// these modules, so window.api / fmtSize / fmtDate / fmtTime are globals here.

import { state, el, status, escape, setActiveCol } from "./dom.js";
import { currentEntry, openEntry } from "./listing.js";

// ---------- Thumbnail sheet (`p`) ----------
// The geometry lives in the CSS (:root --sheet-* in browser.css) and is read
// back here, so the box this fit calculation targets and the box the browser
// actually paints can't drift apart.
export function sheetMetric(name) {
  return parseFloat(
    getComputedStyle(document.documentElement).getPropertyValue(name),
  );
}

// Pick the tile size that fills the most of the 80% box while still fitting
// every frame — that's what keeps the sheet scroll-free at any window size or
// frame count, instead of leaving it to CSS wrapping.
export function layoutSheet() {
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
  el.sheetGrid.style.gridAutoRows = Math.max(1, Math.floor(tw / aspect)) + "px";
}

// A drag-resize fires resize ~60×/s and each layout re-scales up to 60
// images; coalesce to one per frame.
export let sheetResizePending = false;
export function onSheetResize() {
  if (sheetResizePending) return;
  sheetResizePending = true;
  requestAnimationFrame(() => {
    sheetResizePending = false;
    layoutSheet();
  });
}

export async function openSheet() {
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
    res = await api.sheet(state.mountIdx, e.rel_path, state.sheetAbort.signal);
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

export function closeSheet() {
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
export function playSheetShot(shot) {
  const t = parseFloat(shot.dataset.t);
  if (!isFinite(t) || !state.sheetEntry) return;
  openEntry(state.sheetEntry, t);
}

// One handler for mouse and touch: a frame plays from its timestamp, the
// backdrop closes, and the card's own chrome (title, gaps) does neither —
// a stray click while reading the header shouldn't dismiss the sheet.
export function onSheetPointer(ev) {
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
export function cancelSheet() {
  if (state.sheetAbort) {
    state.sheetAbort.abort();
    status("thumbnails cancelled");
  }
}
