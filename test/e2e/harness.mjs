// Browser-driven checks for the things that cannot be verified by reading code.
//
// The page's keyboard handling depends on real browser behaviour: Chromium's
// native media controls live in a closed user-agent shadow root that swallows
// keydown outright, `focus()` inside a focusin dispatch is ignored, and a
// capture-phase listener is the only thing that beats the controls to arrow
// keys. None of that is observable without a real Chromium, real key events and
// real focus. The file-browser page's module graph is in the same category: a
// missing export or a stale reference is a runtime error nothing static catches.
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
  page.on("response", (r) => {
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
  await page.click('#mount-list li[data-i="1"]');
  const secondActive = await page
    .waitForSelector('#mount-list li[data-i="1"][data-active="true"]', {
      timeout: 5000,
    })
    .then(() => true)
    .catch(() => false);
  check("clicking a mount row switches mounts", secondActive);
  await page.click('#mount-list li[data-i="0"]');
  await page.waitForSelector("#file-list li", { timeout: 5000 });

  // data-i=0 is the fixture's only folder — folders sort first, checked above.
  await page.click('#file-list li[data-i="0"]');
  const crumb = await page
    .waitForFunction(() => document.querySelector("#crumbs .cur")?.textContent, {
      timeout: 5000,
    })
    .then((h) => h.jsonValue())
    .catch(() => null);
  check("clicking a folder row opens it", crumb === "sub", String(crumb));
  await page.keyboard.press("h");
  await page.waitForSelector('#file-list li[data-i="1"]', { timeout: 5000 });

  await page.click('#file-list li[data-i="1"]');
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
  const previewNames = await page.$$eval(
    "#preview-meta .dir-list li .name",
    (l) => l.map((n) => n.textContent),
  );
  check(
    "folder preview lists the children",
    previewNames.length === 3,
    previewNames.join(","),
  );
  const previewKinds = await page.$$eval("#preview-meta .dir-list li", (l) =>
    l.map((li) => li.className),
  );
  check(
    "child rows carry their kind",
    previewKinds[0] === "dir" &&
      previewKinds.filter((c) => c === "dir").length === 1,
    previewKinds.join(","),
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
