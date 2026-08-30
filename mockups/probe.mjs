// Ask the page directly whether the mobile media query matched and what the
// module grid resolved to. Looking at a screenshot cannot distinguish "the rule
// did not match" from "the rule matched and I misread the render".
import { spawn } from "node:child_process";
const CHROME = "C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe";
const PORT = 9333;
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

const chrome = spawn(CHROME, [
  `--remote-debugging-port=${PORT}`, "--headless=new", "--disable-gpu",
  "--no-sandbox", "--no-first-run",
  "--user-data-dir=" + process.env.TEMP + "\\cdp-probe", "about:blank",
], { stdio: "ignore" });

let url;
for (let i = 0; i < 40; i++) {
  try {
    url = (await (await fetch(`http://127.0.0.1:${PORT}/json/version`)).json())
      .webSocketDebuggerUrl;
    break;
  } catch { await sleep(250); }
}
const ws = new WebSocket(url);
await new Promise((r) => (ws.onopen = r));
let id = 0; const pend = new Map();
ws.onmessage = (e) => {
  const m = JSON.parse(e.data);
  if (m.id && pend.has(m.id)) { pend.get(m.id)(m.result); pend.delete(m.id); }
};
const send = (method, params, sessionId) => new Promise((r) => {
  const i = ++id; pend.set(i, r);
  ws.send(JSON.stringify({ id: i, method, params, sessionId }));
});

const { targetId } = await send("Target.createTarget", { url: "about:blank" });
const { sessionId } = await send("Target.attachToTarget", { targetId, flatten: true });
await send("Page.enable", {}, sessionId);
await send("Runtime.enable", {}, sessionId);

const CASES = [
  { f: "a-station-board.html", sel: ".mods" },
  { f: "b-rack-panel.html", sel: ".reads" },
  { f: "c-bar-signature.html", sel: ".facts" },
];

for (const w of [390, 1440]) {
  await send("Emulation.setDeviceMetricsOverride",
    { width: w, height: 1200, deviceScaleFactor: 1, mobile: w < 700 }, sessionId);
  for (const c of CASES) {
    await send("Page.navigate",
      { url: `http://localhost:8777/${c.f}?still=1` }, sessionId);
    await sleep(800);
    const expr = `JSON.stringify({
      cw: document.documentElement.clientWidth,
      mq560: matchMedia("(max-width:560px)").matches,
      cols: getComputedStyle(document.querySelector("${c.sel}")).gridTemplateColumns,
      sheets: document.styleSheets.length,
      rules: [...document.styleSheets].reduce((n,s)=>{try{return n+s.cssRules.length}catch{return n}},0)
    })`;
    const { result } = await send("Runtime.evaluate",
      { expression: expr, returnByValue: true }, sessionId);
    console.log(`${c.f} @${w}px ->`, result.value);
  }
}
ws.close(); chrome.kill();
