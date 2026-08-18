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
} from "./listing.js";
import { refreshDisk } from "./disk.js";

// ---------- Modals ----------
export function modal({
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
      await loadDir();
      refreshDisk();
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
    li.addEventListener("click", () => {
      document
        .querySelectorAll("#_sort li")
        .forEach((x) => (x.dataset.selected = "false"));
      li.dataset.selected = "true";
    });
  });
}

export function openFilter() {
  state.focusBeforeFilter = state.focus;
  el.filter.hidden = false;
  el.filterInput.value = state.filter;
  el.filterInput.focus();
  el.filterInput.select();
}
export function closeFilter(commit) {
  el.filter.hidden = true;
  if (!commit) {
    // Restore the pre-filter focus so ESC feels like a true cancel.
    state.filter = "";
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
