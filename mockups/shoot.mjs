// Capture the panel through the DevTools Protocol.
//
// Chrome's --window-size + --screenshot crops a wider layout instead of
// reflowing it, so mobile shots came back as clipped desktop renders and the
// max-width media queries never matched. Emulation.setDeviceMetricsOverride
// sets the real layout viewport; setEmulatedMedia sets the colour scheme that
// --force-prefers-color-scheme silently ignores.
//
// Also measures contrast on the text the craft floor holds to 4.5:1, because
// the design detector runs degraded on this machine and never evaluates it.
//
// Node 22 has a global WebSocket, so this needs no dependencies.
import { spawn } from "node:child_process";
import { mkdir, writeFile } from "node:fs/promises";
import { join } from "node:path";

const CHROME = "C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe";
const OUT = "C:\\Users\\runneradmin\\Desktop\\pro\\dingzi\\.impeccable\\review";
const PORT = 9222;
const TARGET = process.env.DINGZI_URL || "http://localhost:8777/index.html";

const SIZES = [
  { n: "desktop", w: 1440, h: 1250 },
  { n: "mobile", w: 390, h: 1400 },
];
const SCHEMES = ["dark", "light"];

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function wsUrl() {
  for (let i = 0; i < 40; i++) {
    try {
      const r = await fetch(`http://127.0.0.1:${PORT}/json/version`);
      return (await r.json()).webSocketDebuggerUrl;
    } catch { await sleep(250); }
  }
  throw new Error("Chrome did not expose a debugger socket");
}

const chrome = spawn(CHROME, [
  `--remote-debugging-port=${PORT}`, "--headless=new", "--disable-gpu",
  "--no-sandbox", "--hide-scrollbars", "--force-color-profile=srgb",
  "--no-first-run", "--user-data-dir=" + join(OUT, "..", "chrome-profile"),
  "about:blank",
], { stdio: "ignore" });

await mkdir(OUT, { recursive: true });
const ws = new WebSocket(await wsUrl());
await new Promise((r) => (ws.onopen = r));
let id = 0; const pend = new Map();
ws.onmessage = (e) => {
  const m = JSON.parse(e.data);
  if (m.id && pend.has(m.id)) {
    const { res, rej } = pend.get(m.id); pend.delete(m.id);
    m.error ? rej(new Error(m.error.message)) : res(m.result);
  }
};
const send = (method, params = {}, sessionId) => new Promise((res, rej) => {
  const i = ++id; pend.set(i, { res, rej });
  ws.send(JSON.stringify({ id: i, method, params, sessionId }));
});

const { targetId } = await send("Target.createTarget", { url: "about:blank" });
const { sessionId } = await send("Target.attachToTarget", { targetId, flatten: true });
await send("Page.enable", {}, sessionId);
await send("Runtime.enable", {}, sessionId);

// Contrast, measured in the browser against real computed colours rather than
// guessed from source hex.
const AUDIT = `(() => {
  const lin = c => { c /= 255; return c <= 0.03928 ? c/12.92 : ((c+0.055)/1.055)**2.4 };
  const L = s => { const [r,g,b] = s.match(/\\d+(\\.\\d+)?/g).map(Number);
    return 0.2126*lin(r) + 0.7152*lin(g) + 0.0722*lin(b) };
  const ratio = (a,b) => { const x = L(a), y = L(b);
    return ((Math.max(x,y)+0.05)/(Math.min(x,y)+0.05)) };
  const bg = el => { let n = el;
    while (n) { const c = getComputedStyle(n).backgroundColor;
      if (c && c !== "rgba(0, 0, 0, 0)" && !c.startsWith("rgba(0, 0, 0, 0")) return c;
      n = n.parentElement } return getComputedStyle(document.body).backgroundColor };
  const out = [];
  for (const sel of [".num",".num.caution",".num.warn",".sub",".fv",".fv.k",
                     ".mod-k",".fk",".mc-name",".mc-spec",".legend",
                     ".mc.is-warn .mc-flag",".mc-flag.off",
                     ".mc.is-warn .mc-name",".mc.is-warn .mc-spec",
                     ".tally i",".tally b",".clock",".wordmark",
                     ".wordmark small"]) {
    const el = document.querySelector(sel); if (!el) continue;
    const cs = getComputedStyle(el);
    const r = ratio(cs.color, bg(el));
    const px = parseFloat(cs.fontSize);
    const bold = parseInt(cs.fontWeight,10) >= 700;
    const large = px >= 24 || (px >= 18.66 && bold);
    const floor = large ? 3 : 4.5;
    out.push({ sel, px, r: +r.toFixed(2), floor, pass: r >= floor });
  }
  return JSON.stringify(out);
})()`;

const overflow = [], contrast = [];
for (const s of SIZES) {
  for (const sc of SCHEMES) {
    await send("Emulation.setDeviceMetricsOverride", {
      width: s.w, height: s.h, deviceScaleFactor: 1, mobile: s.n === "mobile",
    }, sessionId);
    await send("Emulation.setEmulatedMedia", {
      features: [{ name: "prefers-color-scheme", value: sc }],
    }, sessionId);
    await send("Page.navigate", { url: `${TARGET}?still=1` }, sessionId);
    await sleep(1000);
    const { result: geo } = await send("Runtime.evaluate", {
      expression: `JSON.stringify({sw:document.documentElement.scrollWidth,`
        + `cw:document.documentElement.clientWidth,`
        + `h:document.documentElement.scrollHeight,`
        + `n:document.querySelectorAll(".mc").length})`,
      returnByValue: true,
    }, sessionId);
    const g = JSON.parse(geo.value);
    if (g.sw > g.cw + 1) overflow.push(`${s.n}-${sc}: ${g.sw} > ${g.cw}`);
    if (sc === "light" || sc === "dark") {
      const { result } = await send("Runtime.evaluate",
        { expression: AUDIT, returnByValue: true }, sessionId);
      for (const c of JSON.parse(result.value)) {
        if (!c.pass) contrast.push(`${s.n}-${sc} ${c.sel} ${c.px}px = ${c.r}:1 (needs ${c.floor})`);
      }
    }
    const shot = await send("Page.captureScreenshot",
      { format: "png", captureBeyondViewport: true }, sessionId);
    await writeFile(join(OUT, `panel-${s.n}-${sc}.png`), Buffer.from(shot.data, "base64"));
    console.log(`OK  panel-${s.n}-${sc}  viewport=${g.cw}px  cards=${g.n}  `
      + `height=${g.h}px  per-card=${Math.round(g.h / g.n)}px`);
  }
}
console.log(overflow.length ? "\nOVERFLOW:\n" + overflow.join("\n") : "\nno horizontal overflow");
console.log(contrast.length
  ? "\nCONTRAST BELOW FLOOR:\n" + contrast.join("\n")
  : "\nall audited text clears its contrast floor");
ws.close(); chrome.kill();
