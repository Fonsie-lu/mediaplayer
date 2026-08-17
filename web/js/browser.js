// Entry point for the file-browser page: the single keydown handler, the DOM
// listeners, the mobile nav, and start-up. Everything it calls lives in the
// modules below — this file is the wiring, not the behaviour.
//
// Loaded with <script type="module">, so it runs after the document is parsed
// (the `el` handles in dom.js resolve) and in strict mode.
// Part of the file-browser page. api.js is loaded as a classic script before
// these modules, so window.api / fmtSize / fmtDate / fmtTime are globals here.

import { state, el, ICONS, saveCursor, setActiveCol } from "./dom.js";
import {
  loadStars,
  loadMounts,
  selectMount,
  applyFilter,
  renderFiles,
  updatePreview,
  moveFiles,
  moveMounts,
  enterFocused,
  goUp,
  toggleStar,
} from "./listing.js";
import {
  openSheet,
  closeSheet,
  cancelSheet,
  onSheetPointer,
  playSheetShot,
} from "./sheet.js";
import { refreshDisk, DISK_POLL_MS } from "./disk.js";
import {
  askRename,
  askDelete,
  askSort,
  stripPart,
  applySort,
  toggleFilter,
  closeFilter,
} from "./dialogs.js";

// ---------- Keybinds ----------
document.addEventListener("keydown", (ev) => {
  // Let the browser keep its own shortcuts (ctrl+R, cmd+L, alt+←, …) — same
  // bail as player.js. It matters most for the sheet block below, which
  // swallows every key it doesn't own, and for `q`, whose modified form is an
  // OS quit binding.
  if (ev.ctrlKey || ev.metaKey || ev.altKey) return;
  // The open sheet owns the keyboard. Escape and `q` close it; Tab and
  // Enter/Space still work so the frames can be reached and played without a
  // mouse; every other key is swallowed rather than acting on the file list
  // behind the overlay (`G` would otherwise jump the cursor you can't see).
  // `q` mirrors the player page's close key, scoped to this block: the file
  // list itself leaves `q` unbound (see the `Backspace` case).
  if (!el.sheet.hidden) {
    if (ev.key === "Escape" || ev.key === "q") {
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
    // Backspace, not "q": "q" means close on the player page and in the sheet
    // overlay, and a key that also mutated a file here would be a trap.
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
