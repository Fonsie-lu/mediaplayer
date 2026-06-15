window.api = {
  async json(url, opts) {
    const r = await fetch(
      url,
      Object.assign({ credentials: "same-origin" }, opts || {}),
    );
    const ct = r.headers.get("content-type") || "";
    const data = ct.includes("json") ? await r.json() : await r.text();
    if (!r.ok) throw new Error((data && data.error) || r.statusText);
    return data;
  },
  mounts() {
    return this.json("/api/mounts");
  },
  browse(mount, path, sort) {
    const q = new URLSearchParams({
      mount: String(mount),
      path: path || "",
      sort: sort || "ctime_desc",
    });
    return this.json("/api/browse?" + q);
  },
  rename(mount, path, newName) {
    return this.json("/api/rename", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ mount: String(mount), path, new_name: newName }),
    });
  },
  del(mount, path) {
    return this.json("/api/delete", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ mount: String(mount), path }),
    });
  },
  previewURL(mount, path, size) {
    const q = new URLSearchParams({ mount: String(mount), path });
    if (size) q.set("size", String(size));
    return "/api/preview?" + q;
  },
  probe(mount, path) {
    const q = new URLSearchParams({ mount: String(mount), path });
    return this.json("/api/probe?" + q);
  },
  directURL(mount, path) {
    const q = new URLSearchParams({ mount: String(mount), path });
    return "/api/stream/direct?" + q;
  },
  openStream(mount, path, t, q, audio) {
    const p = new URLSearchParams({
      mount: String(mount),
      path,
      t: String(t || 0),
      q: q || "",
    });
    if (audio != null) p.set("audio", String(audio));
    return this.json("/api/stream/open?" + p);
  },
  closeStream() {
    return this.json("/api/stream/close", { method: "POST" });
  },
  getConfig() {
    return this.json("/api/config");
  },
  saveConfig(cfg) {
    return this.json("/api/config", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(cfg),
    });
  },
};

window.fmtSize = function (n) {
  if (n < 1024) return n + " B";
  const units = ["KB", "MB", "GB", "TB"];
  let v = n / 1024,
    i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return v.toFixed(1) + " " + units[i];
};

window.fmtDate = function (ts) {
  if (!ts) return "";
  const d = new Date(ts * 1000);
  return d.toISOString().slice(0, 10);
};

window.fmtTime = function (sec) {
  if (!isFinite(sec)) return "0:00";
  sec = Math.max(0, Math.floor(sec));
  const h = Math.floor(sec / 3600);
  const m = Math.floor((sec % 3600) / 60);
  const s = sec % 60;
  const pad = (n) => String(n).padStart(2, "0");
  return h ? `${h}:${pad(m)}:${pad(s)}` : `${m}:${pad(s)}`;
};
