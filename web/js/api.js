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
  // post is every mutating endpoint's shape: JSON body, JSON reply. The three
  // callers below differ only in URL and payload.
  post(url, body) {
    return this.json(url, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
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
    return this.post("/api/rename", {
      mount: String(mount),
      path,
      new_name: newName,
    });
  },
  del(mount, path) {
    return this.post("/api/delete", { mount: String(mount), path });
  },
  // version (the file's mtime) only busts the browser cache: the reply is
  // cacheable for a day, and a file re-encoded in place keeps its URL.
  previewURL(mount, path, size, version) {
    const q = new URLSearchParams({ mount: String(mount), path });
    if (size) q.set("size", String(size));
    if (version) q.set("v", String(version));
    return "/api/preview?" + q;
  },
  // mount/path are optional: with them the server reports the filesystem the
  // browsed directory lives on, without them the one named in the config.
  disk(mount, path) {
    if (mount == null) return this.json("/api/disk");
    const q = new URLSearchParams({ mount: String(mount), path: path || "" });
    return this.json("/api/disk?" + q);
  },
  // signal is worth passing: the handler wires the request context into its
  // ffmpegthumbnailer processes, so aborting actually stops the work.
  sheet(mount, path, signal) {
    const q = new URLSearchParams({ mount: String(mount), path });
    return this.json("/api/sheet?" + q, { signal });
  },
  probe(mount, path) {
    const q = new URLSearchParams({ mount: String(mount), path });
    return this.json("/api/probe?" + q);
  },
  directURL(mount, path) {
    const q = new URLSearchParams({ mount: String(mount), path });
    return "/api/stream/direct?" + q;
  },
  // No start offset: the playlist covers the whole video, so the client seeks
  // with standard HLS instead of asking the server to spawn at an offset. (The
  // `t` on the /player *page* URL is unrelated and never reaches the server.)
  // signal lets the player abandon an open it has superseded; the server then
  // skips registering a session nobody would learn the ID of.
  openStream(mount, path, q, audio, signal) {
    const p = new URLSearchParams({ mount: String(mount), path, q: q || "" });
    if (audio != null) p.set("audio", String(audio));
    return this.json("/api/stream/open?" + p, { signal });
  },
  // Sent on the way out of a stream — often out of the page — so it is a
  // beacon (falling back to a keepalive fetch) and nothing waits for it.
  closeStream(session) {
    if (!session) return;
    const url = "/api/stream/close?" + new URLSearchParams({ session });
    try {
      if (navigator.sendBeacon && navigator.sendBeacon(url)) return;
    } catch (_) {}
    fetch(url, { method: "POST", keepalive: true }).catch(() => {});
  },
  stars() {
    return this.json("/api/stars");
  },
  toggleStar(mount, path) {
    return this.post("/api/stars/toggle", { mount: String(mount), path });
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

// Local calendar date — toISOString would give the UTC one, a day off for
// anything made near midnight.
window.fmtDate = function (ts) {
  if (!ts) return "";
  const d = new Date(ts * 1000);
  const pad = (n) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
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
