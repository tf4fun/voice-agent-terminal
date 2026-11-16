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
// code runs without a browser. Values assigned and appended children are kept.
function fake(tag = "") {
  const values = new Map();
  values.set("_tag", tag);
  values.set("_children", []);
  const proxy = new Proxy(function () {}, {
    get(_, key) {
      if (key === Symbol.toPrimitive) return () => "";
      if (key === Symbol.iterator) return function* () { yield* values.get("_children"); };
      if (key === "then" || typeof key === "symbol") return undefined;
      if (key === "append") return (...nodes) => { values.get("_children").push(...nodes); };
      if (key === "replaceChildren") return (...nodes) => { values.set("_children", [...nodes]); };
      if (key === "remove") return () => {};
      if (key === "querySelector") return selector => {
        const cls = selector.startsWith(".") ? selector.slice(1) : "";
        const find = list => {
          for (const child of list) {
            if (!child || typeof child !== "object") continue;
            if (cls && String(child.className || "").split(/\s+/).includes(cls)) return child;
            const nested = find(child._children || []);
            if (nested) return nested;
          }
          return null;
        };
        return find(values.get("_children"));
      };
      if (!values.has(key)) values.set(key, fake());
      return values.get(key);
    },
    set(_, key, value) { values.set(key, value); return true; },
    apply() { return fake(); },
  });
  return proxy;
}

function collectText(node) {
  if (node == null) return "";
  if (typeof node === "string") return node;
  let text = typeof node.textContent === "string" ? node.textContent : "";
  for (const child of node._children || []) text += " " + collectText(child);
  return text.trim();
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
  const doc = fake("document");
  doc.createElement = tag => fake(tag);
  doc.createDocumentFragment = () => fake("fragment");
  doc.createTextNode = text => ({ textContent: String(text), _children: [] });
  const context = {
    console, setTimeout, clearTimeout, Headers, TextEncoder, TextDecoder, AbortController, Intl, crypto, URL,
    document: doc, history: fake(),
    location: { hash: "", pathname: "/" },
    localStorage: { getItem: k => storage.get(k) ?? null, setItem: (k, v) => storage.set(k, v), removeItem: k => storage.delete(k) },
    requestAnimationFrame: cb => { cb(); return 0; },
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
globalThis.app = {
  state, ui, send, showWelcome, openSession, reconcileThread, findMessage,
  reloadReply, handleEvent, retryFailedSend, lastMessageActive, composerChanged, loadSessions,
  TypeError,
};
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

  async "late session metadata response does not overwrite current draft or thread"() {
    const sidA = "77777777-7777-4777-8777-777777777771";
    const sidB = "77777777-7777-4777-8777-777777777772";
    const getA = deferred();
    const getA2 = deferred();
    let callsA = 0;
    const app = await load(base([session(sidB)], [
      [/^GET \/sessions\/77777777-7777-4777-8777-777777777771$/, () => (++callsA === 1 ? getA.promise : getA2.promise)],
      [/^GET \/sessions\/[^/]+\/messages\?/, () => response(200, { items: [], has_more: false })],
    ]));

    // A -> B switch while GET /sessions/A is pending.
    const openA = app.openSession(sidA);
    app.ui.input.value = "draft A";
    await app.openSession(sidB);
    app.ui.input.value = "draft B";
    getA.resolve(response(200, session(sidA)));
    await openA;
    assert.equal(app.state.currentId, sidB);
    assert.equal(app.ui.input.value, "draft B", "B's composer draft stays intact");
    assert.equal(app.state.localDrafts.get(sidA), "draft A", "A's draft stays stored");
    assert.equal(app.calls.filter(c => c.includes(`${sidA}/messages`)).length, 0, "stale A did not fetch messages");

    // A -> New Chat (showWelcome) and fast A -> B -> A.
    app.state.sessions = app.state.sessions.filter(s => s.id !== sidA);
    callsA = 0;
    const getAWelcome = deferred();
    const getAFast = deferred();
    const routesApp = await load(base([session(sidB)], [
      [/^GET \/sessions\/77777777-7777-4777-8777-777777777771$/, () => (++callsA === 1 ? getAWelcome.promise : getAFast.promise)],
      [/^GET \/sessions\/[^/]+\/messages\?/, () => response(200, { items: [], has_more: false })],
    ]));
    const openAW = routesApp.openSession(sidA);
    routesApp.ui.input.value = "draft before welcome";
    routesApp.showWelcome();
    routesApp.ui.input.value = "welcome draft";
    getAWelcome.resolve(response(200, session(sidA)));
    await openAW;
    assert.equal(routesApp.state.currentId, null);
    assert.equal(routesApp.ui.input.value, "welcome draft");

    // Fast A -> B -> A: stale first request resolves after second request.
    routesApp.state.sessions = routesApp.state.sessions.filter(s => s.id !== sidA);
    const staleOpen = routesApp.openSession(sidA);
    routesApp.ui.input.value = "A1";
    await routesApp.openSession(sidB);
    const freshOpen = routesApp.openSession(sidA);
    routesApp.ui.input.value = "A2";
    getAFast.resolve(response(200, session(sidA)));
    await freshOpen;
    // Wait a tick and ensure stale promise didn't touch A2.
    await staleOpen;
    assert.equal(routesApp.state.currentId, sidA);
    assert.equal(routesApp.ui.input.value, "A2");
  },

  async "concurrent reply reloads keep newest revision and consistent loading/deltas"() {
    const sid = "88888888-8888-4888-8888-888888888881";
    const mid = "88888888-8888-4888-8888-888888888882";
    let replyCalls = 0;
    const secondFetch = deferred();
    const app = await load(base([session(sid)], [
      [/^GET \/sessions\/[^/]+\/messages\?/, () => response(200, { items: [meta(mid, sid, "receiving", 6)], has_more: false })],
      [/^GET \/sessions\/[^/]+\/messages\/[^/]+\/content\?part=input$/, () => response(200, "q", { "X-Revision": 6, "X-Message-State": "receiving" })],
      [/^GET \/sessions\/[^/]+\/messages\/[^/]+\/content\?part=reply$/, () => {
        replyCalls++;
        if (replyCalls === 1) return response(200, "init", { "X-Revision": 6, "X-Message-State": "receiving" });
        if (replyCalls === 2) return response(200, "complete text", { "X-Revision": 8, "X-Message-State": "completed" });
        return secondFetch.promise;
      }],
      [/^GET \/sessions\/[^/]+\/messages\/[^/]+$/, () => response(200, meta(mid, sid, "completed", 8))],
    ]));
    await app.openSession(sid);
    const msg = app.findMessage(mid);

    // Concurrent reloadReply + chat.reset + message.updated + delta gap.
    const r1 = app.reloadReply(msg);
    app.handleEvent({ type: "chat.delta", session_id: sid, message_id: mid, offset: 99, text: "late" });
    app.handleEvent({ type: "chat.reset", session_id: sid, message_id: mid });
    const r2 = app.reloadReply(msg);
    assert.equal(msg.loading, true, "loading stays true while reloads run");
    // Older revision 7 arrives after revision 8 already landed.
    secondFetch.resolve(response(200, "stale partial", { "X-Revision": 7, "X-Message-State": "receiving" }));
    await Promise.all([r1, r2]);
    assert.equal(msg.revision, 8);
    assert.equal(msg.state, "completed");
    assert.equal(msg.reply, "complete text");
    assert.equal(msg.loading, false);
    assert.equal(msg.pendingDeltas.length, 0);
    assert.equal(app.ui.send.disabled, true, "empty composer stays disabled, not blocked by stale active state");
    app.ui.input.value = "next";
    app.composerChanged();
    assert.equal(app.ui.send.disabled, false);
  },

  async "failed send preserves recovery identity alongside new draft and uncertain TypeError"() {
    const sid = "99999999-9999-4999-8999-999999999991";
    const sid2 = "99999999-9999-4999-8999-999999999992";
    const posted = [];
    let mode = "busy";
    const sendGate = deferred();
    const app = await load(base([session(sid), session(sid2)], [
      [/^GET \/sessions\/[^/]+\/messages\?/, () => response(200, { items: [], has_more: false })],
      [/^POST \/sessions\/([^/]+)\/messages$/, async (m, opts) => {
        const body = JSON.parse(opts.body);
        posted.push({ session: m[1], ...body });
        if (mode === "busy") { await sendGate.promise; return response(503, { error: { code: "busy" } }); }
        if (mode === "drop") throw new app.TypeError("network lost");
        return response(202, { ...meta(body.id, m[1], "queued", 1), session: session(m[1]) });
      }],
    ]));
    await app.openSession(sid);
    app.ui.input.value = "Message A";
    const sendingA = app.send();
    app.ui.input.value = "Draft B";
    sendGate.resolve();
    await sendingA;
    assert.equal(app.ui.input.value, "Draft B", "B stays in composer");
    const list1 = app.state.failedSends.get(sid) || [];
    assert.equal(list1.length, 1);
    const failedA = list1[0];
    assert.equal(failedA.text, "Message A");

    // Sending B succeeds and does not lose A's recovery entry or ID.
    mode = "ok";
    await app.send();
    assert.equal((app.state.failedSends.get(sid) || []).length, 1);
    assert.equal(app.state.failedSends.get(sid)[0].message, failedA.message);

    // Switch to sid2 and back, then retry A with its exact original message ID.
    await app.openSession(sid2);
    await app.openSession(sid);
    assert.equal((app.state.failedSends.get(sid) || []).length, 1);
    await app.retryFailedSend(failedA);
    assert.equal((app.state.failedSends.get(sid) || []).length, 0);
    const retried = posted.filter(p => p.text === "Message A");
    assert.equal(retried.length, 2);
    assert.equal(retried[0].id, retried[1].id, "retry reused original message ID");

    // Uncertain outcome (TypeError) in New Chat preserves immutable text and IDs independently.
    app.showWelcome();
    mode = "drop";
    app.ui.input.value = "Uncertain A";
    await app.send();
    const freshFailed = app.state.failedSends.get("") || [];
    assert.equal(freshFailed.length, 1);
    assert.equal(freshFailed[0].uncertain, true);
    assert.equal(freshFailed[0].fresh, true);
    assert.equal(app.ui.input.value, "", "uncertain text is kept immutable, not mixed back into composer");
    // Sending a new draft B in New Chat works with its own session ID while A stays recoverable.
    mode = "ok";
    app.ui.input.value = "Fresh B";
    await app.send();
    assert.equal((app.state.failedSends.get("") || []).length, 1);
    await app.retryFailedSend(freshFailed[0]);
    assert.equal((app.state.failedSends.get("") || []).length, 0);
    const retriedFresh = posted.filter(p => p.text === "Uncertain A");
    assert.equal(retriedFresh[0].id, retriedFresh[retriedFresh.length - 1].id);
    assert.equal(retriedFresh[0].session, retriedFresh[retriedFresh.length - 1].session);

    // If an uncertain send actually landed on the server, reconciling clears its failed entry.
    await app.openSession(sid2);
    mode = "drop";
    app.ui.input.value = "Actually landed";
    await app.send();
    const landed = app.state.failedSends.get(sid2) || [];
    assert.equal(landed.length, 1);
    const landedEntry = landed[0];
    // Simulate server returning that message on resync/reconcile.
    const app2 = await load(base([session(sid2)], [
      [/^GET \/sessions\/[^/]+\/messages\?/, () => response(200, { items: [meta(landedEntry.message, sid2, "completed", 2)], has_more: false })],
      [/^GET \/sessions\/[^/]+\/messages\/[^/]+\/content\?part=input$/, () => response(200, "Actually landed", { "X-Revision": 2, "X-Message-State": "completed" })],
      [/^GET \/sessions\/[^/]+\/messages\/[^/]+\/content\?part=reply$/, () => response(200, "done", { "X-Revision": 2, "X-Message-State": "completed" })],
    ]));
    await app2.openSession(sid2);
    app2.state.failedSends.set(sid2, [{ ...landedEntry }]);
    await app2.reconcileThread();
    assert.equal((app2.state.failedSends.get(sid2) || []).length, 0);
  },

  async "attention state blocks sends, displays approval notice and resumes cleanly"() {
    const sid = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa";
    const mid = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb";
    let msgState = "attention";
    let rev = 3;
    let replyBody = "";
    const app = await load(base([session(sid)], [
      [/^GET \/sessions\/[^/]+\/messages\?/, () => response(200, { items: [meta(mid, sid, msgState, rev)], has_more: false })],
      [/^GET \/sessions\/[^/]+\/messages\/[^/]+$/, () => response(200, meta(mid, sid, msgState, rev))],
      [/^GET \/sessions\/[^/]+\/messages\/[^/]+\/content\?part=input$/, () => response(200, "run tool", { "X-Revision": rev, "X-Message-State": msgState })],
      [/^GET \/sessions\/[^/]+\/messages\/[^/]+\/content\?part=reply$/, () => response(200, replyBody, { "X-Revision": rev, "X-Message-State": msgState })],
    ]));
    await app.openSession(sid);
    app.ui.input.value = "follow up";
    app.composerChanged();
    assert.equal(app.lastMessageActive(), true);
    assert.equal(app.ui.send.disabled, true);
    await app.send();
    assert.equal(app.calls.filter(c => c.startsWith("POST ")).length, 0, "send blocked during attention");
    assert.match(collectText(app.ui["thread-inner"]), /Hermes 正在等待审批/);

    // Partial reply under attention also displays the approval notice.
    replyBody = "checking tool...";
    rev = 4;
    await app.reloadReply(app.findMessage(mid));
    assert.match(collectText(app.ui["thread-inner"]), /Hermes 正在等待审批/);

    // Approval granted: completed transition re-enables sends.
    msgState = "completed";
    rev = 5;
    replyBody = "checking tool... done";
    await app.handleEvent({ type: "message.updated", session_id: sid, message_id: mid, status: "completed" });
    await settle();
    assert.equal(app.lastMessageActive(), false);
    assert.equal(app.ui.send.disabled, false);
    assert.equal(app.findMessage(mid).reply, "checking tool... done");
  },
};

let failed = 0;
for (const [name, test] of Object.entries(tests)) {
  try {
    await test();
    console.log(`ok   ${name}`);
  } catch (error) {
    failed++;
    console.log(`FAIL ${name}\n     ${(error.stack || error.message).split("\n").join("\n     ")}`);
  }
}
if (failed) process.exit(1);
console.log("web: PASS");
