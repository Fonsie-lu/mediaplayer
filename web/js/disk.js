// Disk-usage widget: reports on the filesystem the browsed directory sits on,
// and stays hidden rather than showing a number it can't trust.
// Part of the file-browser page. api.js is loaded as a classic script before
// these modules, so window.api / fmtSize / fmtDate / fmtTime are globals here.

import { state, el } from "./dom.js";

// ---------- Disk used space ----------
export const DISK_POLL_MS = 60000;

export async function refreshDisk() {
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
