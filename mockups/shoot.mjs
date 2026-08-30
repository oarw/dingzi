// Capture mockups through the DevTools Protocol.
//
// Chrome's --window-size + --screenshot crops a wider layout instead of
// reflowing it, so mobile shots came back as clipped desktop layouts and the
// max-width media queries never matched. Emulation.setDeviceMetricsOverride
// sets the real layout viewport; setEmulatedMedia sets the colour scheme that
// --force-prefers-color-scheme silently ignores.
//
// Node 22 has a global WebSocket, so this needs no dependencies.
import { spawn } from "node:child_process";
import { mkdir, writeFile } from "node:fs/promises";
import { join } from "node:path";

const CHROME = "C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe";
const OUT = "C:\\Users\\runneradmin\\Desktop\\pro\\dingzi\\.impeccable\\review";
const PORT = 9222;
const BASE = "http://localhost:8777";

const PAGES = [
  { n: "a", f: "a-station-board.html" },
  { n: "b", f: "b-rack-panel.html" },
  { n: "c", f: "c-bar-signature.html" },
];
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

class CDP {
  constructor(ws) {
    this.ws = ws; this.id = 0; this.pending = new Map();
    ws.onmessage = (e) => {
      const m = JSON.parse(e.data);
      if (m.id && this.pending.has(m.id)) {
        const { resolve, reject } = this.pending.get(m.id);
        this.pending.delete(m.id);
        m.error ? reject(new Error(m.error.message)) : resolve(m.result);
      }
    };
  }
  send(method, params = {}, sessionId) {
    const id = ++this.id;
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject });
      this.ws.send(JSON.stringify({ id, method, params, sessionId }));
    });
  }
}

const chrome = spawn(CHROME, [
  `--remote-debugging-port=${PORT}`, "--headless=new", "--disable-gpu",
  "--no-sandbox", "--hide-scrollbars", "--force-color-profile=srgb",
  "--no-first-run", "--user-data-dir=" + join(OUT, "..", "chrome-profile"),
  "about:blank",
], { stdio: "ignore" });

await mkdir(OUT, { recursive: true });
const url = await wsUrl();
const ws = new WebSocket(url);
await new Promise((r) => (ws.onopen = r));
const cdp = new CDP(ws);

const { targetId } = await cdp.send("Target.createTarget", { url: "about:blank" });
const { sessionId } = await cdp.send("Target.attachToTarget", { targetId, flatten: true });
await cdp.send("Page.enable", {}, sessionId);
await cdp.send("Runtime.enable", {}, sessionId);

const overflow = [];
for (const p of PAGES) {
  for (const s of SIZES) {
    for (const sc of SCHEMES) {
      await cdp.send("Emulation.setDeviceMetricsOverride", {
        width: s.w, height: s.h, deviceScaleFactor: 1, mobile: s.n === "mobile",
      }, sessionId);
      await cdp.send("Emulation.setEmulatedMedia", {
        features: [{ name: "prefers-color-scheme", value: sc }],
      }, sessionId);
      await cdp.send("Page.navigate", { url: `${BASE}/${p.f}?still=1` }, sessionId);
      await sleep(900);
      // Overflow is a defect, so measure it rather than eyeball it.
      const { result } = await cdp.send("Runtime.evaluate", {
        expression: `JSON.stringify({sw:document.documentElement.scrollWidth,cw:document.documentElement.clientWidth})`,
        returnByValue: true,
      }, sessionId);
      const { sw, cw } = JSON.parse(result.value);
      if (sw > cw + 1) overflow.push(`${p.n}-${s.n}-${sc}: scrollWidth ${sw} > viewport ${cw}`);
      const shot = await cdp.send("Page.captureScreenshot", {
        format: "png", captureBeyondViewport: true,
      }, sessionId);
      await writeFile(join(OUT, `${p.n}-${s.n}-${sc}.png`), Buffer.from(shot.data, "base64"));
      console.log(`OK   ${p.n}-${s.n}-${sc}  viewport=${cw}px`);
    }
  }
}

console.log(overflow.length ? "\nOVERFLOW:\n" + overflow.join("\n") : "\nno horizontal overflow");
ws.close();
chrome.kill();
