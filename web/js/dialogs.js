// The shared modal and everything built on it (rename, delete, sort), and the
// filter bar's open/close behaviour.
// Part of the file-browser page. api.js is loaded as a classic script before
// these modules, so window.api / fmtSize / fmtDate / fmtTime are globals here.

import { state, el, status, escape, setActiveCol, store } from "./dom.js";
import {
  loadDir,
  currentEntry,
  applyFilter,
  refreshList,
  focusByName,
} from "./listing.js";

// ---------- Modals ----------
function modal({ title, bodyHTML, ok = "OK", cancel = "Cancel", onOk }) {
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
  el.modalCancel.onclick = close;
  const firstInput = el.modalBody.querySelector("input,select,button");
  if (firstInput) firstInput.focus();
}

export function askRename() {
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
      // Follow the entry: a rename changes its ctime and name, so under
      // either sort it may have moved away from the cursor's old index.
      focusByName(v);
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

export function askDelete() {
  const e = currentEntry();
  if (!e) return;
  modal({
    title: "Delete",
    bodyHTML: `<div>Delete <strong>${escape(e.name)}</strong>? This cannot be undone.</div>`,
    ok: "Delete",
    onOk: async () => {
      await api.del(state.mountIdx, e.rel_path);
      await loadDir(); // refreshes the disk widget too
      status("deleted", "ok");
    },
  });
}

export function askSort() {
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
      if (sel) await applySort(sel.dataset.k);
    },
  });
  document.querySelectorAll("#_sort li").forEach((li) => {
    li.addEventListener("click", () => selectSortRow(li));
  });
}

function selectSortRow(li) {
  document
    .querySelectorAll("#_sort li")
    .forEach((x) => (x.dataset.selected = "false"));
  li.dataset.selected = "true";
}

// moveSortSelection is j/k inside the sort dialog, reporting whether one was
// open to act on — its rows aren't focusable, so without this the keyboard
// could only re-apply the current sort.
export function moveSortSelection(delta) {
  const rows = Array.from(document.querySelectorAll("#_sort li"));
  if (el.modal.hidden || !rows.length) return false;
  const cur = rows.findIndex((li) => li.dataset.selected === "true");
  const next = Math.max(0, Math.min(rows.length - 1, cur + delta));
  selectSortRow(rows[next]);
  return true;
}

function openFilter() {
  state.focusBeforeFilter = state.focus;
  state.filterBeforeOpen = state.filter;
  el.filter.hidden = false;
  el.filterInput.value = state.filter;
  el.filterInput.focus();
  el.filterInput.select();
}
export function closeFilter(commit) {
  el.filter.hidden = true;
  if (!commit) {
    // Restore the filter and cursor from before this edit so ESC is a true
    // cancel: the index only means the same row under the same filter.
    state.filter = state.filterBeforeOpen || "";
    applyFilter();
    refreshList(state.focusBeforeFilter || 0);
  }
  el.filterInput.blur();
  setActiveCol("files");
}
export function toggleFilter() {
  if (el.filter.hidden) openFilter();
  else closeFilter(false);
}

export async function applySort(key) {
  state.sort = key;
  store("mp.sort", key);
  status("sort: " + key.replace("_", " "), "ok");
  await loadDir();
}
