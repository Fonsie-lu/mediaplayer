// Browser-driven checks for the things that cannot be verified by reading code.
//
// The page's keyboard handling depends on real browser behaviour: Chromium's
// native media controls live in a closed user-agent shadow root that swallows
// keydown outright, `focus()` inside a focusin dispatch is ignored, and a
// capture-phase listener is the only thing that beats the controls to arrow
// keys. None of that is observable without a real Chromium, real key events and
// real focus. The file-browser page's module graph is in the same category: a
// missing export or a stale reference is a runtime error nothing static catches.
// So is HLS playback: whether hls.js, the server's synthetic playlist and the
// on-demand segments agree only shows up with real MSE playing real segments.
//
// Run with `make e2e` from the repo root. Needs chromium, node and ffmpeg;
// deliberately not part of `make check`, which must run anywhere.

import { spawn, spawnSync } from "node:child_process";
import { mkdtempSync, writeFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import puppeteer from "puppeteer-core";

const CHROME = process.env.CHROME || "/usr/bin/chromium";
const PORT = 18099 + (process.pid % 500);
const BASE = `http://127.0.0.1:${PORT}`;

let passed = 0;
const failures = [];

function check(name, cond, detail = "") {
  if (cond) {
    passed++;
    console.log(`  ok   ${name}`);
  } else {
    failures.push(`${name}${detail ? ` — ${detail}` : ""}`);
    console.log(`  FAIL ${name}${detail ? ` — ${detail}` : ""}`);
  }
}

function section(title) {
  console.log(`\n${title}`);
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// The sheet checks are the only ones that need ffmpegthumbnailer; they are
// skipped where it isn't installed rather than failing the run.
const haveThumbnailer =
  spawnSync("which", ["ffmpegthumbnailer"]).status === 0;

// ---------- fixture ----------

const work = mkdtempSync(join(tmpdir(), "mp-e2e-"));
const media = join(work, "media");
spawnSync("mkdir", ["-p", join(media, "sub")]);

function makeVideo(path, seconds) {
  const r = spawnSync("ffmpeg", [
    "-y",
    "-v",
    "error",
    "-f",
    "lavfi",
    "-i",
    `testsrc2=size=320x180:rate=15:duration=${seconds}`,
    "-f",
    "lavfi",
    "-i",
    `sine=frequency=440:duration=${seconds}`,
    "-c:v",
    "libx264",
    "-preset",
    "ultrafast",
    "-pix_fmt",
    "yuv420p",
    "-g",
    "15",
    "-c:a",
    "aac",
    "-shortest",
    path,
  ]);
  if (r.status !== 0) {
    throw new Error(`ffmpeg failed for ${path}: ${r.stderr}`);
  }
}

// An mpegts recording whose clock starts at 5000s, like any broadcast capture:
// h264 + aac, so the server remuxes it, and a non-zero start time, which is
// what every raw PTS the server reads has to be rebased by. Long enough that a
// seek can land past what hls.js has buffered.
function makeRecording(path, seconds) {
  const r = spawnSync("ffmpeg", [
    "-y",
    "-v",
    "error",
    "-f",
    "lavfi",
    "-i",
    `testsrc2=size=320x180:rate=15:duration=${seconds}`,
    "-f",
    "lavfi",
    "-i",
    `sine=frequency=440:duration=${seconds}`,
    "-c:v",
    "libx264",
    "-preset",
    "ultrafast",
    "-pix_fmt",
    "yuv420p",
    "-g",
    "15",
    "-c:a",
    "aac",
    "-shortest",
    "-output_ts_offset",
    "5000",
    "-f",
    "mpegts",
    path,
  ]);
  if (r.status !== 0) {
    throw new Error(`ffmpeg failed for ${path}: ${r.stderr}`);
  }
}

// An mpeg2video recording: the DVB SD case, and the one the server can neither
// direct-play nor remux, so it exercises the encode path — where a batch's
// first segment carries transcode.segmentLead. Long enough that a seek can
// land past the first batch (16 segments, ~64s) and have to spawn a new one.
function makeMPEG2Recording(path, seconds) {
  const r = spawnSync("ffmpeg", [
    "-y",
    "-v",
    "error",
    "-f",
    "lavfi",
    "-i",
    `testsrc2=size=320x180:rate=25:duration=${seconds}`,
    "-f",
    "lavfi",
    "-i",
    `sine=frequency=440:duration=${seconds}`,
    "-c:v",
    "mpeg2video",
    "-qscale:v",
    "8",
    "-pix_fmt",
    "yuv420p",
    "-g",
    "12",
    "-c:a",
    "aac",
    "-shortest",
    "-output_ts_offset",
    "5000",
    "-f",
    "mpegts",
    path,
  ]);
  if (r.status !== 0) {
    throw new Error(`ffmpeg failed for ${path}: ${r.stderr}`);
  }
}

console.log("building fixture…");
makeVideo(join(media, "alpha.mp4"), 12);
makeVideo(join(media, "beta.mp4"), 8);
writeFileSync(join(media, "notes.txt"), "not a video\n");
writeFileSync(join(media, "gamma.mp4.part"), "partial\n");
// Children of `sub`, for the folder-preview check: a directory and two files,
// so the pane has both row kinds and an order to compare against.
spawnSync("mkdir", ["-p", join(media, "sub", "nested")]);
writeFileSync(join(media, "sub", "one.txt"), "one\n");
writeFileSync(join(media, "sub", "two.txt"), "two\n");
// Inside `nested`, whose own contents no listing check looks at.
makeRecording(join(media, "sub", "nested", "rec.ts"), 90);
makeMPEG2Recording(join(media, "sub", "nested", "rec-mpeg2.ts"), 130);

const cfgPath = join(work, "config.json");
writeFileSync(
  cfgPath,
  JSON.stringify({
    host: "127.0.0.1",
    port: PORT,
    mounts: [
      { name: "testmedia", path: media },
      { name: "second", path: join(media, "sub") },
    ],
  }),
);

// ---------- server ----------

// XDG_CONFIG_HOME is redirected into the scratch dir, not just -config: the
// stars file's path is derived from the config dir with no flag of its own, so
// without this the harness would toggle stars in the developer's real
// ~/.config/mediaplayer-stars.json — and leak state from one run into the next,
// since starring is a toggle.
const server = spawn("./mediaplayer", ["-config", cfgPath, "-no-tui"], {
  cwd: process.cwd(),
  stdio: ["ignore", "pipe", "pipe"],
  env: { ...process.env, XDG_CONFIG_HOME: join(work, "xdg") },
});
const serverLog = [];
server.stdout.on("data", (d) => serverLog.push(String(d)));
server.stderr.on("data", (d) => serverLog.push(String(d)));

async function waitForServer() {
  for (let i = 0; i < 80; i++) {
    try {
      const r = await fetch(`${BASE}/api/mounts`);
      if (r.ok) return true;
    } catch {}
    await sleep(100);
  }
  return false;
}

let browser;
async function cleanup() {
  try {
    await browser?.close();
  } catch {}
  server.kill("SIGKILL");
  rmSync(work, { recursive: true, force: true });
}

// ---------- the checks ----------

try {
  if (!(await waitForServer())) {
    throw new Error(`server never came up:\n${serverLog.join("")}`);
  }

  browser = await puppeteer.launch({
    executablePath: CHROME,
    args: ["--no-sandbox", "--autoplay-policy=no-user-gesture-required"],
  });
  const page = await browser.newPage();
  await page.setViewport({ width: 1280, height: 800 });

  // Any console error or unhandled rejection fails the run: that is what
  // catches a broken module import or a stale cross-module reference.
  const consoleErrors = [];
  page.on("console", (m) => {
    if (m.type() !== "error") return;
    // "Failed to load resource" for a CDN asset is covered by the response hook
    // above, which ignores foreign origins; don't double-report it here.
    if (/Failed to load resource/.test(m.text())) return;
    consoleErrors.push(m.text());
  });
  // Only our own origin counts. hls.js and JetBrains Mono come from CDNs, and a
  // CDN hiccup (or running offline) must not be reported as a page defect —
  // those are documented, deliberately external, and degrade gracefully.
  const ours = (url) => url.startsWith(BASE);
  page.on("requestfailed", (r) => {
    // A media element's request is aborted whenever playback is torn down or
    // the page navigates — normal, and not something the page can avoid.
    const aborted = (r.failure()?.errorText || "").includes("ABORTED");
    if (ours(r.url()) && !aborted) {
      consoleErrors.push(`request failed: ${r.url()}`);
    }
  });
  // A check that provokes errors on purpose sets this to a URL pattern whose
  // failures it expects, for its own duration.
  let tolerated = null;
  page.on("response", (r) => {
    if (tolerated && tolerated.test(r.url())) return;
    // Cursor movement asks for preview thumbnails all through the run, and
    // without ffmpegthumbnailer every one of them 500s. That is the missing
    // tool talking, not the page — the same reason the sheet section skips.
    if (!haveThumbnailer && /\/api\/preview\?/.test(r.url())) return;
    if (ours(r.url()) && r.status() >= 400) {
      consoleErrors.push(`HTTP ${r.status()} ${r.url()}`);
    }
  });
  page.on("pageerror", (e) => consoleErrors.push(String(e.message)));

  // helpers -----------------------------------------------------------------
  const focusedName = () =>
    page
      .$eval('#file-list li[data-focus="true"] .name', (n) => n.textContent)
      .catch(() => null);
  const activeCol = () => page.$eval("#grid", (g) => g.dataset.active);
  const sheetOpen = () => page.$eval("#sheet", (s) => !s.hidden);
  const modalOpen = () => page.$eval("#modal", (m) => !m.hidden);

  section("file browser: load");
  await page.goto(`${BASE}/`, { waitUntil: "networkidle0" });
  await page.waitForSelector("#file-list li", { timeout: 5000 });

  check(
    "page loads with no console errors",
    consoleErrors.length === 0,
    consoleErrors.join(" | "),
  );
  const mountCount = await page.$$eval("#mount-list li", (l) => l.length);
  check("both mounts rendered", mountCount === 2, `got ${mountCount}`);
  const names = await page.$$eval("#file-list li .name", (l) =>
    l.map((n) => n.textContent),
  );
  check(
    "listing shows the fixture files",
    names.includes("alpha.mp4") && names.includes("beta.mp4"),
    names.join(","),
  );
  check("folders sort before files", names[0] === "sub", names[0]);

  section("file browser: navigation keys");
  const first = await focusedName();
  await page.keyboard.press("j");
  const afterJ = await focusedName();
  check("j moves the cursor down", afterJ !== first, `${first} -> ${afterJ}`);
  await page.keyboard.press("k");
  check("k moves it back", (await focusedName()) === first);

  await page.keyboard.press("G");
  const atEnd = await focusedName();
  await page.keyboard.press("g");
  await page.keyboard.press("g");
  check(
    "G jumps to the end and gg back to the top",
    atEnd !== first && (await focusedName()) === first,
    `end=${atEnd}`,
  );

  check("files column is active", (await activeCol()) === "files");
  await page.keyboard.press("Tab");
  check("Tab switches to the mounts column", (await activeCol()) === "mounts");
  await page.keyboard.press("Tab");
  check("Tab switches back", (await activeCol()) === "files");

  section("file browser: q is unbound in the list");
  const urlBefore = page.url();
  await page.keyboard.press("q");
  await sleep(150);
  check(
    "q does not navigate or open anything",
    page.url() === urlBefore && !(await modalOpen()) && !(await sheetOpen()),
  );

  // Row clicks are delegated from the two <ul>s rather than bound per row, so
  // a rebuilt listing must stay clickable — nothing static catches a delegation
  // handler that reads the wrong dataset key.
  section("file browser: delegated row clicks");
  // The file list is rebuilt wholesale by every load, so waiting for a row to
  // merely exist matches a node the rebuild then throws away — the click lands
  // on a detached element and puppeteer reports it as "not clickable". Waiting
  // for the row that identifies the listing under test is what makes the click
  // land on the listing the check is about. It stays a real page.click: that
  // these rows are reachable by an actual pointer is half of what is being
  // tested.
  const clickFileRow = async (i, firstRow) => {
    await page.waitForFunction(
      (want) =>
        document.querySelector('#file-list li[data-i="0"] .name')
          ?.textContent === want,
      { timeout: 5000 },
      firstRow,
    );
    await page.click(`#file-list li[data-i="${i}"]`);
  };

  await page.click('#mount-list li[data-i="1"]');
  const secondActive = await page
    .waitForSelector('#mount-list li[data-i="1"][data-active="true"]', {
      timeout: 5000,
    })
    .then(() => true)
    .catch(() => false);
  check("clicking a mount row switches mounts", secondActive);
  await page.click('#mount-list li[data-i="0"]');

  // data-i=0 is the fixture's only folder — folders sort first, checked above.
  await clickFileRow(0, "sub");
  const crumb = await page
    .waitForFunction(() => document.querySelector("#crumbs .cur")?.textContent, {
      timeout: 5000,
    })
    .then((h) => h.jsonValue())
    .catch(() => null);
  check("clicking a folder row opens it", crumb === "sub", String(crumb));
  await page.keyboard.press("h");

  await clickFileRow(1, "sub");
  const clickedFile = await page
    .waitForFunction(
      () =>
        document.querySelector('#file-list li[data-i="1"]')?.dataset.focus ===
        "true",
      { timeout: 5000 },
    )
    .then(() => focusedName())
    .catch(() => null);
  check(
    "clicking a file row moves the cursor without leaving the page",
    clickedFile !== null && page.url() === urlBefore,
    `${clickedFile} @ ${page.url()}`,
  );

  // The preview column lists a focused folder's children in the listing's
  // active sort order. Asserting against the real listing of that folder rather
  // than a hardcoded order is the point: it is the same sort either way, so a
  // preview that fetched without state.sort (or fetched the wrong directory)
  // shows up as a mismatch whatever the active sort happens to be.
  section("folder preview");
  await page.keyboard.press("g");
  await page.keyboard.press("g");
  check("cursor parked on the folder", (await focusedName()) === "sub");
  await page.waitForSelector("#preview-meta .dir-list li", { timeout: 5000 });
  // One evaluate for all three facts, not three $evals: the pane replaces its
  // innerHTML when a directory fetch lands, and a handle taken between two
  // reads can be detached by that rebuild. Reading inside a single JS turn
  // cannot straddle it.
  //
  // The width is in here because the first version of this pane rendered the
  // filename into a zero-width box: `.preview .meta { width: 100% }` is a
  // descendant selector, so a `meta` span in a child row grew to the full row
  // and squeezed the name out. Only a real layout catches that.
  const preview = await page.evaluate(() => {
    const rows = [...document.querySelectorAll("#preview-meta .dir-list li")];
    return {
      names: rows.map((li) => li.querySelector(".name").textContent),
      kinds: rows.map((li) => li.className),
      nameWidth: rows[0].querySelector(".name").getBoundingClientRect().width,
      nameSize: getComputedStyle(rows[0].querySelector(".name")).fontSize,
      footerSize: getComputedStyle(
        document.querySelector("#preview-meta .dir-total"),
      ).fontSize,
    };
  });
  const previewNames = preview.names;
  check(
    "folder preview lists the children",
    previewNames.length === 3,
    previewNames.join(","),
  );
  check(
    "child rows carry their kind",
    preview.kinds[0] === "dir" &&
      preview.kinds.filter((c) => c === "dir").length === 1,
    preview.kinds.join(","),
  );
  check(
    "child names have a visible width",
    preview.nameWidth > 20,
    `${preview.nameWidth}px`,
  );
  // Child rows are 15px against the pane's 13px meta size; the footer is not.
  check(
    "child rows are larger than the tally footer",
    parseFloat(preview.nameSize) > parseFloat(preview.footerSize),
    `${preview.nameSize} vs ${preview.footerSize}`,
  );
  await page.keyboard.press("Enter");
  await page.waitForFunction(
    () => document.querySelector("#crumbs .cur")?.textContent === "sub",
    { timeout: 5000 },
  );
  const insideNames = await page.$$eval("#file-list li .name", (l) =>
    l.map((n) => n.textContent),
  );
  check(
    "preview order matches the folder's own listing",
    insideNames.join(",") === previewNames.join(","),
    `${previewNames.join(",")} vs ${insideNames.join(",")}`,
  );
  await page.keyboard.press("h");
  await page.waitForFunction(() => !document.querySelector("#crumbs .cur"), {
    timeout: 5000,
  });
  // Back on a file, the pane goes back to the thumbnail: no stale child rows.
  await page.keyboard.press("G");
  await page.waitForFunction(
    () => !document.querySelector("#preview-meta .dir-list"),
    { timeout: 5000 },
  );
  check("moving off the folder clears the child list", true);

  section("thumbnail sheet");
  // The sheet is the one section that shells out to ffmpegthumbnailer. Missing,
  // it is skipped rather than failed — the same treatment the HLS checks get
  // when hls.js can't be fetched, and for the same reason: a tool this machine
  // doesn't have is not a defect in the page.
  if (!haveThumbnailer) {
    console.log("  skip  ffmpegthumbnailer not installed — sheet checks not run");
  } else {
    // Park the cursor on a video, then render the sheet.
    await page.evaluate(() => {
      const items = [...document.querySelectorAll("#file-list li")];
      const i = items.findIndex(
        (li) => li.querySelector(".name").textContent === "alpha.mp4",
      );
      items[i].dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
      items[i].dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    check("cursor parked on the video", (await focusedName()) === "alpha.mp4");

    await page.keyboard.press("p");
    await page.waitForFunction(() => !document.getElementById("sheet").hidden, {
      timeout: 20000,
    });
    check("p opens the sheet", await sheetOpen());
    const shots = await page.$$eval(".sheet-shot", (l) => l.length);
    check("sheet rendered at least one frame", shots >= 1, `got ${shots}`);
    const focusIsShot = await page.evaluate(() =>
      document.activeElement?.classList.contains("sheet-shot"),
    );
    check("focus parked on the first frame", focusIsShot === true);

    // Tab must wrap inside the overlay rather than escaping to the page behind.
    await page.keyboard.press("Tab");
    const stillInSheet = await page.evaluate(() =>
      document.activeElement?.classList.contains("sheet-shot"),
    );
    check("Tab stays inside the sheet", stillInSheet === true);

    // The modifier bail: ctrl+q must not close it (it is an OS quit chord).
    await page.keyboard.down("Control");
    await page.keyboard.press("q");
    await page.keyboard.up("Control");
    await sleep(100);
    check("ctrl+q does NOT close the sheet", await sheetOpen());

    // A key the sheet does not own must not reach the list behind it.
    const focusUnderneath = await focusedName();
    await page.keyboard.press("G");
    await sleep(100);
    check(
      "G is swallowed while the sheet is open",
      (await focusedName()) === focusUnderneath,
    );

    // Now the binding this all exists for.
    await page.keyboard.press("q");
    await page.waitForFunction(() => document.getElementById("sheet").hidden, {
      timeout: 3000,
    });
    check("q closes the sheet", !(await sheetOpen()));

    // And Escape still does too.
    await page.keyboard.press("p");
    await page.waitForFunction(() => !document.getElementById("sheet").hidden, {
      timeout: 20000,
    });
    await page.keyboard.press("Escape");
    await page.waitForFunction(() => document.getElementById("sheet").hidden, {
      timeout: 3000,
    });
    check("Escape closes the sheet", !(await sheetOpen()));
  }

  section("dialogs");
  await page.keyboard.press("r");
  await page.waitForFunction(() => !document.getElementById("modal").hidden, {
    timeout: 3000,
  });
  check("r opens the rename dialog", await modalOpen());
  // q inside a dialog is typed text, not a close key.
  await page.keyboard.press("q");
  await sleep(100);
  check("q does not close a dialog with a text input", await modalOpen());
  await page.keyboard.press("Escape");
  await page.waitForFunction(() => document.getElementById("modal").hidden, {
    timeout: 3000,
  });
  check("Escape closes the dialog", !(await modalOpen()));

  section("filter");
  await page.keyboard.press("/");
  await page.waitForFunction(() => !document.getElementById("filter").hidden, {
    timeout: 3000,
  });
  await page.keyboard.type("beta");
  await page.waitForFunction(
    () => document.querySelectorAll("#file-list li").length === 1,
    { timeout: 3000 },
  );
  const filtered = await page.$$eval("#file-list li .name", (l) =>
    l.map((n) => n.textContent),
  );
  check(
    "filter narrows the listing",
    filtered.length === 1 && filtered[0] === "beta.mp4",
    filtered.join(","),
  );
  await page.keyboard.press("Escape");
  await page.waitForFunction(
    () => document.querySelectorAll("#file-list li").length > 1,
    { timeout: 3000 },
  );
  check("Escape clears the filter", true);

  section("stars & disk widget");
  await page.keyboard.press("y");
  await page.waitForFunction(
    () => document.querySelector("#file-list .star") !== null,
    { timeout: 3000 },
  );
  check("y stars the focused entry", true);
  // Server-side: a reload must still show it.
  await page.reload({ waitUntil: "networkidle0" });
  await page.waitForSelector("#file-list li");
  const starAfterReload = await page.$("#file-list .star");
  check("star survives a reload (server-side)", starAfterReload !== null);
  const diskVisible = await page.$eval("#disk", (d) => !d.hidden);
  check("disk widget resolved the browsed filesystem", diskVisible === true);

  section("player page");
  const playerErrors = [];
  page.on("pageerror", (e) => playerErrors.push(String(e.message)));
  await page.goto(
    `${BASE}/player?mount=0&path=${encodeURIComponent("alpha.mp4")}`,
    { waitUntil: "networkidle0" },
  );
  await page.waitForSelector("#video", { timeout: 5000 });
  await page.waitForFunction(
    () => {
      const v = document.getElementById("video");
      return v && (v.readyState >= 2 || v.currentSrc);
    },
    { timeout: 20000 },
  );
  check(
    "player page loads a source with no page errors",
    playerErrors.length === 0,
    playerErrors.join(" | "),
  );

  // The focusin bounce, and it has to be driven by a real click: `:focus-visible`
  // is the signal the page uses, so keyboard-driven focus is deliberately kept
  // and only pointer-driven focus is bounced. Clicking the video's own surface is
  // the case that used to leave every later keystroke inside Chromium's closed
  // shadow root.
  const box = await page.$eval("#video", (v) => {
    const r = v.getBoundingClientRect();
    return { x: r.x + r.width / 2, y: r.y + r.height / 4 };
  });
  await page.mouse.click(box.x, box.y);
  await sleep(80);
  const focusAfterClick = await page.evaluate(
    () => document.activeElement?.id || "(none)",
  );
  check(
    "pointer focus bounces off the video element",
    focusAfterClick !== "video",
    `activeElement=${focusAfterClick}`,
  );

  // Keyboard focus is the other half of the contract: Tab must still be able to
  // land on the video, or the page becomes unreachable by keyboard.
  await page.evaluate(() => {
    const v = document.getElementById("video");
    v.focus();
  });
  await sleep(80);

  // With focus off the video, the page's own shortcuts must work.
  const t0 = await page.$eval("#video", (v) => v.currentTime);
  await page.keyboard.press("ArrowRight");
  await sleep(300);
  const t1 = await page.$eval("#video", (v) => v.currentTime);
  check("ArrowRight seeks", t1 !== t0, `${t0} -> ${t1}`);

  await page.keyboard.press("?");
  await sleep(150);
  const helpShown = await page
    .$eval("#help", (h) => !h.hidden)
    .catch(() => null);
  if (helpShown !== null) {
    check("? opens the shortcut card", helpShown === true);
    await page.keyboard.press("q");
    await sleep(150);
    const helpClosed = await page.$eval("#help", (h) => h.hidden);
    check(
      "q closes the shortcut card before leaving the page",
      helpClosed === true && page.url().includes("/player"),
    );
  }

  // HLS needs hls.js, which comes from a CDN: offline, these are skipped
  // rather than failed, like the CDN errors above.
  section("player page: HLS");
  const segRequests = [];
  let playlistURL = null;
  const onSegRequest = (r) => {
    const m = r.url().match(/\/seg_(\d+)\.ts$/);
    if (m) segRequests.push(Number(m[1]));
    if (/\/playlist\.m3u8$/.test(r.url())) playlistURL = r.url();
  };
  page.on("request", onSegRequest);
  // Each batch start is one log line, which is how a check counts the ffmpeg
  // runs a seek cost.
  const countBatches = () => {
    const lines = serverLog.join("").split("\n");
    return lines.filter((l) => / batch seg /.test(l)).length;
  };
  await page.goto(
    `${BASE}/player?mount=0&path=${encodeURIComponent("sub/nested/rec.ts")}&t=20`,
    { waitUntil: "domcontentloaded" },
  );
  const hlsAvailable = await page
    .waitForFunction(() => window.Hls || window.__hlsjsFailed, {
      timeout: 15000,
    })
    .then(() => page.evaluate(() => !!window.Hls && window.Hls.isSupported()))
    .catch(() => false);
  if (!hlsAvailable) {
    console.log("  skip  hls.js unavailable (offline?) — HLS checks not run");
  } else {
    const playing = (past) =>
      page
        .waitForFunction(
          (t) => document.getElementById("video").currentTime > t,
          { timeout: 30000 },
          past,
        )
        .then(() => true)
        .catch(() => false);
    const statusText = () => page.$eval("#status", (s) => s.textContent);

    // The recording's PTS start at 5000s. Unrebased, every keyframe sat past
    // the duration and the file fell back to a full transcode.
    const started = await playing(21);
    check(
      "an mpegts recording with an offset clock remuxes and plays",
      started && /^remux/.test(await statusText()),
      await statusText(),
    );
    // Asking hls.js to start at ?t= directly: fetching segment 0 first made
    // the server spawn a batch there only to kill it for the real position.
    check(
      "a ?t= start never fetches segment 0",
      segRequests.length > 0 && !segRequests.includes(0),
      `segments requested: ${segRequests.slice(0, 6).join(",")}`,
    );

    // A server restart or the idle reaper drops the session under a playing
    // page; the next segment 404s and the player must reopen, not die.
    tolerated = /\/api\/stream\/hls\//;
    await page.evaluate(() => fetch("/api/stream/close", { method: "POST" }));
    await page.$eval("#video", (v) => (v.currentTime = 75));
    check(
      "a lost session is reopened and playback carries on",
      (await playing(76)) && /^remux/.test(await statusText()),
      await statusText(),
    );
    tolerated = null;

    // A seek onto an exact segment boundary is the case that used to cost two
    // ffmpeg batches and sometimes never played: the media a segment carries
    // began a few tens of ms after the playlist position it was advertised at,
    // so the playhead landed in a hole and hls.js went off to fetch the
    // *previous* segment — which stops the batch just started for the seek
    // target and spawns another. The fix gives each batch's first segment a
    // lead, and what proves it is the buffer: after the seek the buffered
    // range has to start at or before the position asked for.
    //
    // It runs on the mpeg2video fixture because the lead is encode-mode only
    // (remux keeps its seek exactly on the segment start — see segmentLead),
    // and the target is past the first batch so that a new one has to spawn.
    await page.goto(
      `${BASE}/player?mount=0&path=${encodeURIComponent("sub/nested/rec-mpeg2.ts")}`,
      { waitUntil: "domcontentloaded" },
    );
    const encodeStarted = await playing(1);
    check(
      "an mpeg2 recording transcodes and plays",
      encodeStarted && /^transcode/.test(await statusText()),
      await statusText(),
    );
    // Segment starts out of the server's own playlist, read from inside the
    // page so the session cookie applies.
    const bounds = playlistURL
      ? await page.evaluate(async (url) => {
          const res = await fetch(url, { credentials: "same-origin" });
          if (!res.ok) return null;
          const out = [];
          let t = 0;
          for (const line of (await res.text()).split("\n")) {
            if (line.startsWith("#EXTINF:")) {
              out.push(t);
              t += parseFloat(line.slice(8));
            }
          }
          return out;
        }, playlistURL)
      : null;
    const boundary = bounds && bounds[20];
    if (!boundary) {
      check(
        "playlist gave a segment boundary to seek to",
        false,
        String(boundary),
      );
    } else {
      const batchesBefore = countBatches();
      await page.$eval("#video", (v, t) => (v.currentTime = t), boundary);
      const resumed = await playing(boundary + 0.4);
      const bufStart = await page.$eval("#video", (v) =>
        v.buffered.length ? v.buffered.start(0) : -1,
      );
      check(
        "a seek onto a segment boundary resumes playback",
        resumed,
        `at ${boundary}s, status "${await statusText()}"`,
      );
      check(
        "the segment covers the boundary it is advertised at",
        bufStart >= 0 && bufStart <= boundary,
        `buffered starts ${bufStart.toFixed(3)}, sought ${boundary.toFixed(3)}`,
      );
      // The whole point: the player got what it needed from one batch, instead
      // of sending the server back to re-encode from a segment earlier.
      const spawned = countBatches() - batchesBefore;
      check(
        "and costs a single ffmpeg batch",
        spawned === 1,
        `${spawned} batches spawned`,
      );
    }

    // The server's direct verdict can't know this browser. Serve the direct
    // request something no browser can decode: the player must fall back to
    // HLS instead of sitting on a black frame.
    await page.setRequestInterception(true);
    const garbage = (r) => {
      if (r.isInterceptResolutionHandled()) return;
      if (r.url().includes("/api/stream/direct")) {
        r.respond({
          status: 200,
          contentType: "video/mp4",
          body: "not a video",
        });
      } else {
        r.continue();
      }
    };
    page.on("request", garbage);
    await page.goto(
      `${BASE}/player?mount=0&path=${encodeURIComponent("alpha.mp4")}`,
      { waitUntil: "domcontentloaded" },
    );
    const fellBack = await page
      .waitForFunction(
        () =>
          /direct play unsupported/.test(
            document.getElementById("status").textContent,
          ) && document.getElementById("video").currentTime > 0.5,
        { timeout: 30000 },
      )
      .then(() => true)
      .catch(() => false);
    check(
      "a direct file the browser rejects falls back to HLS",
      fellBack,
      await statusText(),
    );
    page.off("request", garbage);
    await page.setRequestInterception(false);
  }
  page.off("request", onSegRequest);

  section("no stray errors overall");
  check(
    "no console errors across the whole run",
    consoleErrors.length === 0,
    consoleErrors.slice(0, 3).join(" | "),
  );
} catch (err) {
  failures.push(`harness error: ${err.message}`);
  console.log(`\nharness error: ${err.stack}`);
} finally {
  await cleanup();
}

console.log(`\n${passed} passed, ${failures.length} failed`);
if (failures.length) {
  console.log("\nfailures:");
  for (const f of failures) console.log(`  - ${f}`);
  process.exit(1);
}
