// Drives the panel's HTTP and WebSocket surface against a running server.
//
// Node 22 has WebSocket and fetch built in, so this needs no dependencies. It
// exists because the terminal path crosses HTTP auth, a cookie, a WebSocket
// upgrade and an agent round trip, and none of those are visible to a Go unit
// test of any single piece.
//
// Usage: node check.mjs <baseURL> <password> <machineID>

const [base, password, machineID] = process.argv.slice(2);
if (!base || !password || !machineID) {
  console.error("usage: node check.mjs <baseURL> <password> <machineID>");
  process.exit(2);
}

let failures = 0;
function report(name, ok, detail) {
  console.log(`${ok ? "PASS" : "FAIL"}  ${name}${detail ? " — " + detail : ""}`);
  if (!ok) failures++;
}

// --- fleet API ---------------------------------------------------------------

const fleetRes = await fetch(`${base}/api/v1/servers`);
const fleet = await fleetRes.json();
report("GET /api/v1/servers returns 200", fleetRes.status === 200,
  `status ${fleetRes.status}`);
report("panel reports terminal_enabled", fleet.terminal_enabled === true,
  `got ${fleet.terminal_enabled}`);
report("fleet has one machine", fleet.servers?.length === 1,
  `got ${fleet.servers?.length}`);

const m = fleet.servers?.[0];
if (m) {
  report("machine is online", m.online === true, `online=${m.online}`);
  report("agent reports terminal capability", m.terminal_enabled === true,
    `terminal_enabled=${m.terminal_enabled}`);
  report("cpu is a plausible percentage", m.cpu >= 0 && m.cpu <= 100, `cpu=${m.cpu}`);
  report("memory total is populated", m.mem_total > 0, `mem_total=${m.mem_total}`);
  report("uptime is populated", m.uptime > 0, `uptime=${m.uptime}`);
  report("clock skew is not flagged", !m.skew_ms, `skew_ms=${m.skew_ms ?? 0}`);
}

// --- auth --------------------------------------------------------------------

const badLogin = await fetch(`${base}/api/v1/login`, {
  method: "POST",
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify({ password: "definitely-wrong" }),
});
report("wrong password is rejected", badLogin.status === 401,
  `status ${badLogin.status}`);

// The panel throttles login attempts to one per second per client, so a correct
// password immediately after a wrong one is legitimately rejected with 429.
// Waiting out the window is the test adapting to the design, not working around
// a bug: the throttle is what makes an online brute force impractical.
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
report("rapid retry is throttled",
  (await fetch(`${base}/api/v1/login`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ password }),
  })).status === 429,
  "a second attempt inside the throttle window");

await sleep(1200);

const login = await fetch(`${base}/api/v1/login`, {
  method: "POST",
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify({ password }),
});
report("correct password logs in", login.status === 200, `status ${login.status}`);

const rawCookie = login.headers.getSetCookie?.()[0] ?? "";
const cookie = rawCookie.split(";")[0];
report("login sets a session cookie", cookie.startsWith("dingzi_session="), cookie);
report("session cookie is HttpOnly", /HttpOnly/i.test(rawCookie), rawCookie);
report("session cookie is SameSite=Lax", /SameSite=Lax/i.test(rawCookie), rawCookie);

// --- mutations require the session ------------------------------------------

const unauthPatch = await fetch(`${base}/api/v1/servers/${machineID}`, {
  method: "PATCH",
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify({ name: "hacked" }),
});
report("unauthenticated PATCH is rejected", unauthPatch.status === 401,
  `status ${unauthPatch.status}`);

async function patch(body) {
  return fetch(`${base}/api/v1/servers/${machineID}`, {
    method: "PATCH",
    headers: { "Content-Type": "application/json", Cookie: cookie },
    body: JSON.stringify(body),
  });
}

report("rename works", (await patch({ name: "renamed-box" })).status === 200);
report("invalid count_mode is rejected",
  (await patch({ count_mode: "nonsense" })).status === 400);
report("reset_day 0 is rejected", (await patch({ reset_day: 0 })).status === 400);
report("reset_day 32 is rejected", (await patch({ reset_day: 32 })).status === 400);
report("empty name is rejected", (await patch({ name: "" })).status === 400);
report("quota and reset day are accepted",
  (await patch({ quota: 1099511627776, reset_day: 15, count_mode: "out" })).status === 200);

const missing = await fetch(`${base}/api/v1/servers/999999`, {
  method: "PATCH",
  headers: { "Content-Type": "application/json", Cookie: cookie },
  body: JSON.stringify({ name: "ghost" }),
});
report("PATCH on a missing machine is 404", missing.status === 404,
  `status ${missing.status}`);

const after = await (await fetch(`${base}/api/v1/servers`)).json();
const updated = after.servers?.[0];
report("rename persisted", updated?.name === "renamed-box", `name=${updated?.name}`);
report("quota persisted", updated?.quota === 1099511627776, `quota=${updated?.quota}`);
report("count mode persisted", updated?.quota_mode === "out",
  `quota_mode=${updated?.quota_mode}`);
report("reset day persisted", updated?.reset_day === 15,
  `reset_day=${updated?.reset_day}`);

// --- history -----------------------------------------------------------------

const hist = await (await fetch(
  `${base}/api/v1/servers/${machineID}/history?hours=1&buckets=50`)).json();
report("history returns points", Array.isArray(hist.points),
  `points=${hist.points?.length}`);

// --- terminal ----------------------------------------------------------------

function openTerminal(withCookie) {
  return new Promise((resolve) => {
    const url = `${base.replace(/^http/, "ws")}/api/v1/servers/${machineID}` +
      `/terminal?cols=100&rows=30`;
    const ws = new WebSocket(url, {
      headers: withCookie ? { Cookie: cookie } : {},
    });
    const frames = [];
    const timer = setTimeout(() => {
      ws.close();
      resolve({ frames, timedOut: true });
    }, 25000);

    ws.addEventListener("message", (ev) => {
      if (typeof ev.data === "string") {
        frames.push(JSON.parse(ev.data));
        clearTimeout(timer);
        ws.close();
        resolve({ frames, timedOut: false });
      }
    });
    ws.addEventListener("error", () => {
      clearTimeout(timer);
      resolve({ frames, error: true });
    });
    ws.addEventListener("close", () => {
      clearTimeout(timer);
      resolve({ frames, closed: true });
    });
  });
}

const unauthTerm = await openTerminal(false);
report("unauthenticated terminal is refused",
  unauthTerm.error === true || unauthTerm.frames.length === 0,
  JSON.stringify(unauthTerm.frames));

const term = await openTerminal(true);
const first = term.frames[0];
report("terminal request reaches the agent and is answered",
  first !== undefined, term.timedOut ? "timed out with no frame" : "");

if (first) {
  // On Windows the agent has no pty, so the correct outcome is a readable
  // refusal rather than a shell. On unix this is a ready frame instead. Either
  // proves the whole path worked; a timeout would not.
  const isNotice = first.type === "notice";
  const isReady = first.type === "ready";
  report("answer is a notice or a ready frame", isNotice || isReady,
    JSON.stringify(first));
  if (isNotice) {
    report("refusal carries a reason an operator can act on",
      typeof first.message === "string" && first.message.length > 0,
      first.message);
    console.log(`      agent said: ${first.message}`);
  }
  if (isReady) {
    console.log(`      shell attached: ${first.shell}`);
  }
}

console.log(`\n${failures === 0 ? "all checks passed" : failures + " check(s) failed"}`);
process.exit(failures === 0 ? 0 : 1);
