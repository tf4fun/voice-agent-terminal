#!/usr/bin/env node
// Behaviour tests for the web client. app.js runs unchanged in a VM context
// with a permissive fake DOM and a scripted fetch; no packages are needed.
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import vm from "node:vm";

const source = readFileSync(join(dirname(fileURLToPath(import.meta.url)),
  "../server/internal/httpapi/web/app.js"), "utf8");

// Any property is another fake node and any call returns one, so rendering
// code runs without a browser. Values assigned are kept.
function fake() {
  const values = new Map();
  return new Proxy(function () {}, {
    get(_, key) {
      if (key === Symbol.toPrimitive) return () => "";
      if (key === "then" || typeof key === "symbol") return undefined;
      if (!values.has(key)) values.set(key, fake());
      return values.get(key);
    },
    set(_, key, value) { values.set(key, value); return true; },
    apply() { return fake(); },
  });
}

function response(status, body, headers = {}) {
  const json = typeof body !== "string";
  const map = new Map(Object.entries({ "Content-Type": json ? "application/json" : "text/plain", ...headers })
    .map(([k, v]) => [k.toLowerCase(), String(v)]));
  return {
    status, ok: status >= 200 && status < 300, body: null,
    headers: { get: name => map.get(name.toLowerCase()) ?? null },
    json: async () => body, text: async () => body,
  };
}

const session = id => ({ id, title: "t", state: "ready", created_at: 1, updated_at: 1 });
const meta = (id, sessionId, state, revision, extra = {}) =>
  ({ id, session_id: sessionId, state, revision, created_at: 1, updated_at: 1, input_bytes: 1, reply_bytes: 1, ...extra });

// Loads the page with a gateway whose routes the test supplies.
async function load(routes) {
  const toasts = [];
  const calls = [];
  const storage = new Map();
  const context = {
    console, setTimeout, clearTimeout, Headers, TextEncoder, TextDecoder, AbortController, Intl, crypto, URL,
    document: fake(), history: fake(),
    location: { hash: "", pathname: "/" },
    localStorage: { getItem: k => storage.get(k) ?? null, setItem: (k, v) => storage.set(k, v), removeItem: k => storage.delete(k) },
    requestAnimationFrame: () => 0,
    fetch: async (url, options = {}) => {
      const method = options.method || "GET";
      const path = url.replace(/^\/api\/v1/, "");
      calls.push(`${method} ${path}`);
      if (path === "/events") return new Promise(() => {});
      for (const [pattern, handler] of routes) {
        const match = `${method} ${path}`.match(pattern);
        if (match) return handler(match, options);
      }
      return response(404, { error: { code: "not_found" } });
    },
  };
  context.window = { matchMedia: () => ({ matches: false }), addEventListener() {}, innerHeight: 800 };
  vm.createContext(context);
  vm.runInContext(source + `
globalThis.app = { state, ui, send, showWelcome, openSession, reconcileThread, findMessage };
globalThis.toast = text => app.toasts.push(text);`, context);
  context.app.toasts = toasts;
  context.app.calls = calls;
  await settle();
  return context.app;
}

const settle = () => new Promise(resolve => setTimeout(resolve, 20));
function deferred() {
  let resolve;
  const promise = new Promise(r => { resolve = r; });
  return { promise, resolve };
}

const base = (sessions, extra = []) => [
  ...extra,
  [/^GET \/server$/, () => response(200, { service: "voice-agent-server" })],
  [/^GET \/sessions\?/, () => response(200, { items: sessions, has_more: false })],
  [/^GET \/sessions\/([^/?]+)$/, m => response(200, session(m[1]))],
];

const tests = {
  async "a send keeps text typed while it is in flight"() {
    const sid = "11111111-1111-4111-8111-111111111111";
    const pending = deferred();
    const app = await load(base([session(sid)], [
      [/^GET \/sessions\/[^/]+\/messages\?/, () => response(200, { items: [], has_more: false })],
      [/^POST \/sessions\/[^/]+\/messages$/, () => pending.promise],
    ]));
    await app.openSession(sid);
    app.ui.input.value = "A";
    const sending = app.send();
    assert.equal(app.ui.input.value, "", "the sent text leaves the composer");
    app.ui.input.value = "B";
    pending.resolve(response(201, meta("22222222-2222-4222-8222-222222222222", sid, "queued", 1)));
    await sending;
    assert.equal(app.ui.input.value, "B");
    assert.deepEqual(app.toasts, []);
  },

  async "a first send keeps the new-chat draft typed meanwhile"() {
    const pending = deferred();
    let created = "";
    const app = await load(base([], [
      [/^GET \/sessions\/[^/]+\/messages\?/, () => response(200, { items: [], has_more: false })],
      [/^POST \/sessions\/([^/]+)\/messages$/, m => { created = m[1]; return pending.promise; }],
    ]));
    app.ui.input.value = "A";
    const sending = app.send();
    app.ui.input.value = "B";
    pending.resolve(response(201, { ...meta("33333333-3333-4333-8333-333333333333", created, "queued", 1), session: session(created) }));
    await sending;
    assert.equal(app.state.currentId, created, "the new session opens");
    assert.equal(app.state.localDrafts.get(""), "B", "the new-chat draft survives");
    app.showWelcome();
    assert.equal(app.ui.input.value, "B");
  },

  async "a failed send puts its text back"() {
    const sid = "44444444-4444-4444-8444-444444444444";
    const app = await load(base([session(sid)], [
      [/^GET \/sessions\/[^/]+\/messages\?/, () => response(200, { items: [], has_more: false })],
      [/^POST \/sessions\/[^/]+\/messages$/, () => response(503, { error: { code: "busy" } })],
    ]));
    await app.openSession(sid);
    app.ui.input.value = "C";
    await app.send();
    assert.equal(app.ui.input.value, "C");
    assert.equal(app.toasts.length, 1);
  },

  async "a reconnect refetches a reply whose revision changed in the same state"() {
    const sid = "55555555-5555-4555-8555-555555555555";
    const mid = "66666666-6666-4666-8666-666666666666";
    let revision = 7, reply = "partial";
    const app = await load(base([session(sid)], [
      [/^GET \/sessions\/[^/]+\/messages\?/, () => response(200, { items: [meta(mid, sid, "completed", revision)], has_more: false })],
      [/^GET \/sessions\/[^/]+\/messages\/[^/]+\/content\?part=input$/, () => response(200, "q", { "X-Revision": revision, "X-Message-State": "completed" })],
      [/^GET \/sessions\/[^/]+\/messages\/[^/]+\/content\?part=reply$/, () => response(200, reply, { "X-Revision": revision, "X-Message-State": "completed" })],
    ]));
    await app.openSession(sid);
    assert.equal(app.findMessage(mid).reply, "partial");
    await app.reconcileThread();
    const fetches = () => app.calls.filter(c => c.endsWith("part=reply")).length;
    assert.equal(fetches(), 1, "an unchanged message is not refetched");
    revision = 8;
    reply = "partial and the rest";
    await app.reconcileThread();
    assert.equal(fetches(), 2);
    assert.equal(app.findMessage(mid).reply, "partial and the rest");
    assert.deepEqual(app.toasts, [], "reconciling reports no error");
  },
};

let failed = 0;
for (const [name, test] of Object.entries(tests)) {
  try {
    await test();
    console.log(`ok   ${name}`);
  } catch (error) {
    failed++;
    console.log(`FAIL ${name}\n     ${error.message.split("\n").join("\n     ")}`);
  }
}
if (failed) process.exit(1);
console.log("web: PASS");
