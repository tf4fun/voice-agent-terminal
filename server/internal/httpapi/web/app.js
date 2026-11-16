"use strict";

// Voice Agent web client. Talks to the same gateway API as the device, so the
// conversation, the shared draft and live replies stay consistent with it.

const $ = id => document.getElementById(id);
const ui = {};
for (const id of [
  "app", "sidebar", "close-sidebar", "open-sidebar", "scrim", "new-chat", "search", "session-list",
  "connection", "connection-dot", "theme", "sign-out", "title", "title-state", "rename", "delete",
  "thread", "thread-inner", "live", "live-text", "composer", "input", "send", "draft-note",
  "auth", "auth-form", "api-key", "remember", "auth-error", "rename-dialog", "rename-form",
  "rename-input", "rename-cancel", "rename-confirm", "delete-dialog", "delete-name", "delete-cancel",
  "delete-confirm", "toast", "device-logs", "logs-dialog", "logs-back", "logs-close", "logs-title",
  "logs-note", "logs-list", "logs-text",
]) ui[id] = $(id);

const KEY_STORE = "voice-agent.key";
const THEME_STORE = "voice-agent.theme";
const PAGE = 20;
const ACTIVE = new Set(["queued", "sending", "waiting", "receiving"]);
const RETRYABLE = new Set(["failed", "unconfirmed"]);
const DEFAULT_TITLE = "新会话";
const encoder = new TextEncoder();
const byteLength = text => encoder.encode(text).length;

const state = {
  key: "",
  authed: false,
  server: null,
  sessions: [],
  sessionsMore: false,
  sessionsLoading: false,
  currentId: null,
  messages: [],
  messagesMore: false,
  threadSequence: 0,
  draft: { text: "", revision: 0, dirty: false, sequence: 0 },
  localDrafts: new Map(),
  sending: false,
  events: null,
  live: { segment: "", timer: 0, fetching: false, again: false },
};

/* ---------- storage, theme, helpers ---------- */

function storageGet(name) { try { return localStorage.getItem(name) || ""; } catch { return ""; } }
function storageSet(name, value) {
  try { if (value) localStorage.setItem(name, value); else localStorage.removeItem(name); } catch { /* private mode */ }
}

const THEMES = ["system", "light", "dark"];
const THEME_LABEL = { system: "跟随系统", light: "浅色", dark: "深色" };
function applyTheme(theme) {
  if (!THEMES.includes(theme)) theme = "system";
  document.documentElement.dataset.theme = theme;
  ui.theme.title = "主题：" + THEME_LABEL[theme];
  ui.theme.setAttribute("aria-label", "切换主题（当前：" + THEME_LABEL[theme] + "）");
}

function uuid() {
  const b = crypto.getRandomValues(new Uint8Array(16));
  b[6] = (b[6] & 0x0f) | 0x40;
  b[8] = (b[8] & 0x3f) | 0x80;
  const h = Array.from(b, x => x.toString(16).padStart(2, "0")).join("");
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`;
}

const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));

function el(tag, className, text) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
}

const ICONS = {
  copy: "M8 8h10v12H8zM6 16H5V4h10v1",
  check: "M5 12l5 5 9-10",
  retry: "M4 12a8 8 0 0 1 14-5.3M20 4v5h-5M20 12a8 8 0 0 1-14 5.3M4 20v-5h5",
  edit: "M5 19h4L19 9l-4-4L5 15zM13 7l4 4",
  trash: "M5 7h14M10 7V5h4v2M7 7l1 12h8l1-12",
};
function icon(name) {
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("viewBox", "0 0 24 24");
  svg.setAttribute("aria-hidden", "true");
  const path = document.createElementNS("http://www.w3.org/2000/svg", "path");
  path.setAttribute("d", ICONS[name]);
  svg.append(path);
  return svg;
}

function mark() {
  const node = el("span", "mark");
  node.setAttribute("aria-hidden", "true");
  node.append(el("i"), el("i"), el("i"));
  return node;
}

let toastTimer = 0;
function toast(text) {
  ui.toast.textContent = text;
  ui.toast.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { ui.toast.hidden = true; }, 3600);
}

async function copyText(text) {
  try {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(text);
      return true;
    }
  } catch { /* fall back below */ }
  // Plain-HTTP LAN pages have no async clipboard; use a hidden selection.
  const area = el("textarea");
  area.value = text;
  area.setAttribute("readonly", "");
  area.style.position = "fixed";
  area.style.opacity = "0";
  document.body.append(area);
  area.select();
  let ok = false;
  try { ok = document.execCommand("copy"); } catch { ok = false; }
  area.remove();
  return ok;
}

function copyButton(getText, label = "复制", caption = "") {
  const button = el("button");
  button.type = "button";
  button.title = label;
  button.setAttribute("aria-label", label);
  const rest = () => button.replaceChildren(icon("copy"), el("span", "", caption));
  rest();
  button.addEventListener("click", async () => {
    const ok = await copyText(getText());
    button.replaceChildren(icon(ok ? "check" : "copy"), el("span", "", ok ? "已复制" : caption));
    if (!ok) toast("复制失败，请手动选择文本");
    setTimeout(rest, 1600);
  });
  return button;
}

/* ---------- time ---------- */

const timeFormat = new Intl.DateTimeFormat("zh-CN", { hour: "2-digit", minute: "2-digit" });
const dayFormat = new Intl.DateTimeFormat("zh-CN", { month: "long", day: "numeric" });
const fullFormat = new Intl.DateTimeFormat("zh-CN", { dateStyle: "medium", timeStyle: "short" });
function startOfDay(date) { const d = new Date(date); d.setHours(0, 0, 0, 0); return d.getTime(); }
function dayDiff(timestamp) { return Math.round((startOfDay(Date.now()) - startOfDay(timestamp)) / 86400000); }
function shortTime(timestamp) {
  if (!timestamp) return "";
  const diff = dayDiff(timestamp);
  if (diff <= 0) return timeFormat.format(timestamp);
  if (diff === 1) return "昨天";
  return dayFormat.format(timestamp);
}
function groupName(timestamp) {
  const diff = dayDiff(timestamp);
  if (diff <= 0) return "今天";
  if (diff === 1) return "昨天";
  if (diff < 7) return "近 7 天";
  if (diff < 30) return "近 30 天";
  return "更早";
}

/* ---------- API ---------- */

const ERRORS = {
  busy: "会话正忙：设备可能正在录音，或上一条消息仍在处理。",
  stale_revision: "草稿刚刚被更新（可能来自设备），请确认后再发送。",
  conflict: "会话当前状态不允许此操作，请稍后再试。",
  not_found: "会话不存在或已被删除。",
  limit_exceeded: "内容超过服务端上限（64 KiB）。",
  request_too_large: "内容过长。",
  hermes_not_configured: "服务端未配置 Hermes。",
  hermes_session_error: "Hermes 暂时无法同步，请稍后重试。",
  invalid_title: "标题无效。",
  subscriber_limit: "实时连接数已满，请关闭其他页面。",
};

function apiError(status, code) {
  const error = new Error(ERRORS[code] || (status >= 500 ? `服务暂时不可用（${status}）` : `请求失败（${status}）`));
  error.status = status;
  error.code = code || "";
  return error;
}

function authHeaders(extra) {
  const headers = new Headers(extra);
  if (state.key) headers.set("Authorization", "Bearer " + state.key);
  return headers;
}

async function request(path, options = {}) {
  const headers = authHeaders({ Accept: "application/json" });
  let body;
  if (options.json !== undefined) {
    headers.set("Content-Type", "application/json");
    body = JSON.stringify(options.json);
  }
  const response = await fetch("/api/v1" + path, {
    method: options.method || "GET", headers, body, signal: options.signal, cache: "no-store",
  });
  const type = response.headers.get("Content-Type") || "";
  const data = type.includes("application/json") ? await response.json().catch(() => ({})) : await response.text();
  if (!response.ok) {
    if (response.status === 401) requireKey(state.key ? "访问密钥无效或已更改，请重新输入。" : "");
    throw apiError(response.status, data && data.error && data.error.code);
  }
  if (options.raw) return { data, headers: response.headers, status: response.status };
  return data;
}

function report(error) {
  if (!error || error.name === "AbortError" || error.status === 401) return;
  toast(error instanceof TypeError ? "无法连接服务，请检查网络或服务是否运行。" : error.message);
}

/* ---------- authentication ---------- */

function requireKey(message) {
  state.authed = false;
  state.key = "";
  storageSet(KEY_STORE, "");
  stopEvents();
  ui.auth.hidden = false;
  ui["auth-error"].textContent = message || "";
  ui["auth-error"].hidden = !message;
  ui["sign-out"].hidden = true;
  setConnection("需要访问密钥", "");
  if (state.currentId) setRoute(state.currentId); // Reopen the same session after signing in again.
  setTimeout(() => ui["api-key"].focus(), 0);
}

async function connect() {
  try {
    state.server = await request("/server");
  } catch (error) {
    if (error.status === 401) return false;
    setConnection("无法连接服务", "bad");
    report(error);
    setTimeout(connect, 5000);
    return false;
  }
  state.authed = true;
  ui.auth.hidden = true;
  ui["sign-out"].hidden = !state.key;
  setConnection(serverLabel(), "ok");
  startEvents();
  await loadSessions(true);
  const routed = routeId();
  if (routed) await openSession(routed, { fromRoute: true });
  else showWelcome();
  return true;
}

function serverLabel() {
  const s = state.server;
  if (!s) return "已连接";
  const parts = ["已连接", "v" + s.version];
  if (!s.chat) parts.push("Hermes 未配置");
  if (!s.transcription) parts.push("转写未配置");
  return parts.join(" · ");
}

function setConnection(text, tone) {
  ui.connection.textContent = text;
  ui["connection-dot"].className = "dot" + (tone ? " " + tone : "");
}

/* ---------- event stream (fetch-based so it can send the bearer key) ---------- */

function stopEvents() {
  if (state.events) state.events.abort();
  state.events = null;
}

async function startEvents() {
  stopEvents();
  const controller = new AbortController();
  state.events = controller;
  let backoff = 1000;
  while (!controller.signal.aborted) {
    let watchdog = 0;
    const local = new AbortController();
    const abortLocal = () => local.abort();
    controller.signal.addEventListener("abort", abortLocal, { once: true });
    const arm = () => { clearTimeout(watchdog); watchdog = setTimeout(abortLocal, 35000); };
    try {
      const response = await fetch("/api/v1/events", { headers: authHeaders({ Accept: "text/event-stream" }), signal: local.signal, cache: "no-store" });
      if (response.status === 401) { requireKey("访问密钥无效或已更改，请重新输入。"); return; }
      if (!response.ok || !response.body) throw apiError(response.status, "");
      setConnection(serverLabel(), "ok");
      backoff = 1000;
      arm();
      const reader = response.body.getReader();
      const decoder = new TextDecoder();
      let buffer = "";
      for (;;) {
        const { value, done } = await reader.read();
        if (done) break;
        arm();
        buffer += decoder.decode(value, { stream: true });
        let split;
        while ((split = buffer.indexOf("\n\n")) >= 0) {
          const frame = buffer.slice(0, split);
          buffer = buffer.slice(split + 2);
          for (const line of frame.split("\n")) {
            if (!line.startsWith("data:")) continue;
            try { handleEvent(JSON.parse(line.slice(5).trim())); } catch (error) { console.warn("event", error); }
          }
        }
      }
    } catch (error) {
      if (controller.signal.aborted) return;
    } finally {
      clearTimeout(watchdog);
      controller.signal.removeEventListener("abort", abortLocal);
    }
    if (controller.signal.aborted) return;
    setConnection("正在重新连接…", "bad");
    await sleep(backoff);
    backoff = Math.min(backoff * 2, 15000);
  }
}

let sessionsRefreshTimer = 0;
function refreshSessionsSoon() {
  clearTimeout(sessionsRefreshTimer);
  sessionsRefreshTimer = setTimeout(() => loadSessions(false).catch(report), 400);
}

let draftRefreshTimer = 0;
function refreshDraftSoon() {
  clearTimeout(draftRefreshTimer);
  draftRefreshTimer = setTimeout(() => loadDraft().catch(report), 250);
}

function handleEvent(event) {
  switch (event.type) {
    case "resync":
      refreshSessionsSoon();
      if (state.currentId) reconcileThread();
      break;
    case "session.updated":
      refreshSessionsSoon();
      break;
    case "draft.updated":
      if (event.session_id === state.currentId) {
        trackLive(event);
        refreshDraftSoon();
      }
      refreshSessionsSoon();
      break;
    case "chat.delta":
      if (event.session_id === state.currentId) applyDelta(event);
      break;
    case "chat.reset":
      if (event.session_id === state.currentId) {
        const message = findMessage(event.message_id);
        if (message) reloadReply(message);
      }
      break;
    case "message.updated":
      if (event.session_id === state.currentId) messageUpdated(event);
      refreshSessionsSoon();
      break;
  }
}

/* ---------- sessions ---------- */

async function loadSessions(reset) {
  if (state.sessionsLoading && !reset) return;
  state.sessionsLoading = true;
  try {
    const wanted = reset ? PAGE : Math.max(PAGE, state.sessions.length);
    const items = [];
    let more = false;
    for (let offset = 0; offset < wanted; offset += PAGE) {
      const page = await request(`/sessions?limit=${PAGE}&offset=${offset}`);
      items.push(...page.items);
      more = page.has_more;
      if (!more) break;
    }
    state.sessions = items;
    state.sessionsMore = more;
    renderSessions();
    syncHeader();
    if (state.sessions.some(s => s.state === "deleting")) refreshSessionsSoonLater();
  } finally {
    state.sessionsLoading = false;
  }
}

let deletingTimer = 0;
function refreshSessionsSoonLater() {
  clearTimeout(deletingTimer);
  deletingTimer = setTimeout(() => loadSessions(false).catch(report), 2000);
}

async function loadMoreSessions() {
  if (!state.sessionsMore || state.sessionsLoading) return;
  state.sessionsLoading = true;
  try {
    const page = await request(`/sessions?limit=${PAGE}&offset=${state.sessions.length}`);
    const known = new Set(state.sessions.map(s => s.id));
    state.sessions.push(...page.items.filter(s => !known.has(s.id)));
    state.sessionsMore = page.has_more;
    renderSessions();
  } catch (error) {
    report(error);
  } finally {
    state.sessionsLoading = false;
  }
}

function currentSession() { return state.sessions.find(s => s.id === state.currentId) || null; }

function sessionNote(session) {
  if (session.state === "deleting") return { text: "正在删除…", busy: true };
  if (session.state === "creating") return { text: session.error ? "等待 Hermes 连接" : "正在建立…", busy: true };
  return { text: shortTime(session.updated_at), busy: false };
}

function renderSessions() {
  const query = ui.search.value.trim().toLowerCase();
  const list = state.sessions
    .filter(s => !query || (s.title || "").toLowerCase().includes(query))
    .sort((a, b) => (b.updated_at || 0) - (a.updated_at || 0));
  const fragment = document.createDocumentFragment();
  let group = "";
  for (const session of list) {
    const name = groupName(session.updated_at || session.created_at);
    if (name !== group) {
      group = name;
      fragment.append(el("div", "group-label", name));
    }
    fragment.append(sessionRow(session));
  }
  if (!list.length) {
    fragment.append(el("div", "list-empty", query ? "没有匹配的会话" : "还没有会话。在下方输入消息，或在设备上长按 OK 开始。"));
  }
  if (state.sessionsMore) {
    const more = el("button", "list-more", query ? "加载更多会话以继续搜索" : "加载更多");
    more.type = "button";
    more.addEventListener("click", loadMoreSessions);
    fragment.append(more);
  }
  ui["session-list"].replaceChildren(fragment);
}

function sessionRow(session) {
  const row = el("div", "row" + (session.id === state.currentId ? " active" : ""));
  const main = el("button", "row-main");
  main.type = "button";
  const title = session.title || "未命名会话";
  const badge = el("span", "badge", Array.from(title)[0] || "·");
  badge.setAttribute("aria-hidden", "true");
  const text = el("span", "row-text");
  const note = sessionNote(session);
  text.append(el("span", "row-title", title), el("span", "row-sub" + (note.busy ? " busy" : ""), note.text));
  main.title = title;
  main.append(badge, text);
  if (session.id === state.currentId) main.setAttribute("aria-current", "page");
  main.addEventListener("click", () => { openSession(session.id); closeSidebar(); });
  const tools = el("span", "row-tools");
  if (session.state !== "deleting") {
    const rename = el("button");
    rename.type = "button";
    rename.title = "重命名";
    rename.setAttribute("aria-label", "重命名 " + title);
    rename.append(icon("edit"));
    rename.addEventListener("click", () => openRename(session));
    const remove = el("button");
    remove.type = "button";
    remove.title = "删除";
    remove.setAttribute("aria-label", "删除 " + title);
    remove.append(icon("trash"));
    remove.addEventListener("click", () => openDelete(session));
    tools.append(rename, remove);
  }
  row.append(main, tools);
  return row;
}

function syncHeader() {
  const session = currentSession();
  ui.title.textContent = session ? (session.title || "未命名会话") : (state.currentId ? "会话" : "新对话");
  document.title = session ? `${session.title || "会话"} · Voice Agent` : "Voice Agent";
  ui.rename.hidden = ui.delete.hidden = !session || session.state === "deleting";
  const note = session && session.state !== "ready" ? sessionNote(session).text : "";
  ui["title-state"].textContent = note;
  ui["title-state"].hidden = !note;
}

/* ---------- routing ---------- */

function routeId() {
  const match = location.hash.match(/^#\/c\/([0-9a-f-]{36})$/);
  return match ? match[1] : "";
}
function setRoute(id) {
  const hash = id ? "#/c/" + id : "";
  if (location.hash !== hash) history.replaceState(null, "", hash || location.pathname);
}

/* ---------- thread ---------- */

function rememberLocalDraft() {
  if (state.currentId === undefined) return;
  const key = state.currentId || "";
  if (state.draft.dirty || (!state.currentId && ui.input.value)) state.localDrafts.set(key, ui.input.value);
  else state.localDrafts.delete(key);
}

function showWelcome(options = {}) {
  rememberLocalDraft();
  state.currentId = null;
  state.threadSequence++;
  state.messages = [];
  state.messagesMore = false;
  state.draft = { text: "", revision: 0, dirty: false, sequence: state.draft.sequence + 1 };
  setLive(null);
  if (!options.keepRoute) setRoute("");
  syncHeader();
  renderSessions();
  const hour = new Date().getHours();
  const greeting = hour < 6 ? "夜深了" : hour < 11 ? "早上好" : hour < 13 ? "中午好" : hour < 18 ? "下午好" : "晚上好";
  const welcome = el("div", "welcome");
  const big = mark();
  big.classList.add("large");
  welcome.append(big, el("h2", "", `${greeting}，有什么可以帮你？`),
    el("p", "", "这里和设备共用同一组会话。设备上长按 OK 说话，草稿也会同步出现在输入框里。"));
  const cards = el("div", "welcome-cards");
  for (const [title, hint] of [
    ["帮我整理今天的待办", "按优先级列出来"],
    ["解释一个概念", "用通俗的例子说明"],
    ["写一段简短的消息", "语气友好、简洁"],
    ["规划一次周末出行", "包含交通和时间安排"],
  ]) {
    const card = el("button", "welcome-card");
    card.type = "button";
    card.append(el("span", "", title), el("small", "", hint));
    card.addEventListener("click", () => {
      ui.input.value = title + "：";
      composerChanged();
      ui.input.focus();
    });
    cards.append(card);
  }
  welcome.append(cards);
  ui["thread-inner"].replaceChildren(welcome);
  ui.input.value = state.localDrafts.get("") || "";
  composerChanged();
}

async function openSession(id, options = {}) {
  if (id === state.currentId && !options.force) return;
  rememberLocalDraft();
  state.currentId = id;
  const sequence = ++state.threadSequence;
  state.messages = [];
  state.messagesMore = false;
  state.draft = { text: "", revision: 0, dirty: false, sequence: state.draft.sequence + 1 };
  setLive(null);
  setRoute(id);
  if (!currentSession()) {
    try {
      const session = await request(`/sessions/${id}`);
      if (!state.sessions.some(s => s.id === id)) state.sessions.push(session);
    } catch (error) {
      if (sequence !== state.threadSequence) return;
      if (options.fromRoute) { showWelcome(); return; }
      report(error);
      return;
    }
  }
  syncHeader();
  renderSessions();
  const local = state.localDrafts.get(id);
  ui.input.value = local || "";
  state.draft.dirty = !!local;
  composerChanged();
  const loading = el("div", "state-line");
  loading.append(typing(), el("span", "", "正在载入对话…"));
  ui["thread-inner"].replaceChildren(loading);
  try {
    await Promise.all([loadMessages(id, sequence, 0), loadDraft()]);
  } catch (error) {
    if (sequence === state.threadSequence) {
      ui["thread-inner"].replaceChildren(el("div", "state-line error", "对话载入失败"));
      report(error);
    }
    return;
  }
  if (sequence !== state.threadSequence) return;
  renderThread();
  scrollToBottom(true);
  if (window.matchMedia("(min-width: 861px)").matches) ui.input.focus();
}

async function mapLimit(items, limit, task) {
  const results = new Array(items.length);
  let next = 0;
  await Promise.all(Array.from({ length: Math.min(limit, items.length) }, async () => {
    while (next < items.length) {
      const index = next++;
      results[index] = await task(items[index]);
    }
  }));
  return results;
}

function makeMessage(meta) {
  return {
    id: meta.id, state: meta.state, error: meta.error || "", created: meta.created_at,
    input: "", reply: "", replyBytes: 0, loading: false, pendingDeltas: [], node: null, frame: 0,
  };
}

async function fetchContent(message) {
  const base = `/sessions/${state.currentId}/messages/${message.id}/content?part=`;
  const [input, reply] = await Promise.all([request(base + "input", { raw: true }), request(base + "reply", { raw: true })]);
  message.input = input.data;
  setReply(message, reply.data, reply.headers.get("X-Message-State") || message.state);
}

function setReply(message, text, stateName) {
  message.reply = text;
  message.replyBytes = byteLength(text);
  if (stateName) message.state = stateName;
  const deltas = message.pendingDeltas;
  message.pendingDeltas = [];
  for (const delta of deltas) applyDeltaTo(message, delta);
}

async function loadMessages(id, sequence, offset) {
  const page = await request(`/sessions/${id}/messages?limit=${PAGE}&offset=${offset}`);
  const metas = page.items.slice().reverse();
  const loaded = metas.map(makeMessage);
  for (const message of loaded) message.loading = true;
  if (sequence !== state.threadSequence) return [];
  // Track the newest page immediately so live deltas buffer while content loads.
  if (offset === 0) state.messages = loaded;
  await mapLimit(loaded, 4, async message => {
    try { await fetchContent(message); } finally { message.loading = false; }
  });
  if (sequence !== state.threadSequence) return [];
  if (offset > 0) {
    const known = new Set(state.messages.map(m => m.id));
    state.messages = loaded.filter(m => !known.has(m.id)).concat(state.messages);
  }
  state.messagesMore = page.has_more;
  return loaded;
}

async function loadOlder(button) {
  const id = state.currentId;
  const sequence = state.threadSequence;
  button.disabled = true;
  button.textContent = "正在载入…";
  const anchor = ui.thread.scrollHeight - ui.thread.scrollTop;
  try {
    await loadMessages(id, sequence, state.messages.length);
    if (sequence !== state.threadSequence) return;
    renderThread();
    ui.thread.style.scrollBehavior = "auto";
    ui.thread.scrollTop = ui.thread.scrollHeight - anchor;
    ui.thread.style.scrollBehavior = "";
  } catch (error) {
    report(error);
    button.disabled = false;
    button.textContent = "更早的消息";
  }
}

// After a reconnect the stream may have missed events; refetch the newest page.
// Calls made while one runs coalesce into a single follow-up pass.
let reconciling = null;
let reconcileAgain = false;
function reconcileThread() {
  if (reconciling) { reconcileAgain = true; return reconciling; }
  reconciling = reconcileOnce().finally(() => {
    reconciling = null;
    if (reconcileAgain) { reconcileAgain = false; reconcileThread(); }
  });
  return reconciling;
}

async function reconcileOnce() {
  const id = state.currentId;
  const sequence = state.threadSequence;
  try {
    const page = await request(`/sessions/${id}/messages?limit=${PAGE}&offset=0`);
    if (sequence !== state.threadSequence) return;
    for (const meta of page.items.slice().reverse()) {
      let message = findMessage(meta.id);
      if (!message) {
        // Sent from the device or another page: show it and follow, like a new turn here.
        message = makeMessage(meta);
        state.messages.push(message);
        message.loading = true;
        try { await fetchContent(message); } finally { message.loading = false; }
        if (sequence !== state.threadSequence) return;
        renderThread();
        scrollToBottom(true); // Instant, so later deltas still see the view at the bottom.
        continue;
      }
      if (message.state !== meta.state || ACTIVE.has(meta.state)) {
        message.state = meta.state;
        message.error = meta.error || "";
        await reloadReply(message);
      }
    }
    loadDraft().catch(report);
  } catch (error) {
    report(error);
  }
}

function findMessage(id) { return state.messages.find(m => m.id === id); }

async function reloadReply(message) {
  const id = state.currentId;
  message.loading = true;
  try {
    const reply = await request(`/sessions/${id}/messages/${message.id}/content?part=reply`, { raw: true });
    if (id !== state.currentId) return;
    setReply(message, reply.data, reply.headers.get("X-Message-State") || message.state);
  } catch (error) {
    report(error);
  } finally {
    message.loading = false;
  }
  updateMessage(message);
}

function applyDelta(event) {
  const message = findMessage(event.message_id);
  if (!message) {
    // A message sent from the device: pick it up with its full content.
    reconcileThread();
    return;
  }
  if (message.loading) { message.pendingDeltas.push(event); return; }
  if (applyDeltaTo(message, event)) {
    if (message.state !== "receiving" && ACTIVE.has(message.state)) message.state = "receiving";
    updateMessage(message);
  }
}

// Deltas carry UTF-8 byte offsets; skip duplicates and refetch after a gap.
function applyDeltaTo(message, event) {
  const offset = event.offset || 0;
  const text = event.text || "";
  if (offset === message.replyBytes) {
    message.reply += text;
    message.replyBytes += byteLength(text);
    return true;
  }
  if (offset + byteLength(text) <= message.replyBytes) return false;
  reloadReply(message);
  return false;
}

async function messageUpdated(event) {
  let message = findMessage(event.message_id);
  if (!message) { reconcileThread(); return; }
  if (event.status) message.state = event.status;
  if (!ACTIVE.has(message.state)) {
    try {
      const meta = await request(`/sessions/${state.currentId}/messages/${message.id}`);
      message.error = meta.error || "";
      message.state = meta.state;
    } catch { /* keep the event state */ }
    await reloadReply(message);
  } else {
    updateMessage(message);
  }
  composerChanged();
}

function renderThread() {
  const fragment = document.createDocumentFragment();
  if (state.messagesMore) {
    const older = el("button", "older", "更早的消息");
    older.type = "button";
    older.addEventListener("click", () => loadOlder(older));
    fragment.append(older);
  }
  if (!state.messages.length) {
    const empty = el("div", "welcome");
    const big = mark();
    big.classList.add("large");
    empty.append(big, el("h2", "", "开始这段对话"), el("p", "", "在下方输入消息，或在设备上长按 OK 说话。"));
    fragment.append(empty);
  }
  state.messages.forEach((message, index) => {
    const nodes = messageNodes(message, index === state.messages.length - 1);
    fragment.append(...nodes);
  });
  ui["thread-inner"].replaceChildren(fragment);
  composerChanged();
}

function messageNodes(message, last) {
  const user = el("div", "msg user");
  user.append(el("div", "bubble", message.input || "（空消息）"));
  const userTools = el("div", "msg-tools");
  userTools.append(el("span", "msg-time", message.created ? fullFormat.format(message.created) : ""), copyButton(() => message.input));
  user.append(userTools);
  const assistant = el("div", "msg assistant" + (last ? " last" : ""));
  message.node = assistant;
  fillAssistant(message, assistant);
  return [user, assistant];
}

function typing() {
  const node = el("span", "typing");
  node.setAttribute("aria-hidden", "true");
  node.append(el("i"), el("i"), el("i"));
  return node;
}

const STATE_TEXT = {
  attention: "Hermes 正在等待审批，请在 Hermes 中处理。",
  incomplete: "回复未完成。",
  cancelled: "回复已取消。",
  failed: "回复失败。",
  unconfirmed: "结果未确认：连接中断或服务重启。",
};
const ERROR_TEXT = {
  hermes_identity_changed: "Hermes 的地址或密钥已变更，确认后可重新发送。",
};

function fillAssistant(message, node) {
  const head = el("div", "assistant-head");
  head.append(mark(), el("strong", "", "Hermes"));
  const body = el("div", "reply md");
  const active = ACTIVE.has(message.state);
  if (message.reply) {
    body.append(renderMarkdown(message.reply));
    if (active) {
      const last = body.lastElementChild;
      (last && !last.classList.contains("code") ? last : body).classList.add("caret");
    }
  } else if (active) {
    const line = el("div", "state-line");
    line.append(typing(), el("span", "", message.state === "receiving" ? "正在回复…" : "正在思考…"));
    body.append(line);
  } else if (message.state === "completed") {
    body.append(el("p", "muted", "（没有文字回复）"));
  }
  const problem = STATE_TEXT[message.state];
  if (problem && !active) {
    const line = el("p", "state-line error", ERROR_TEXT[message.error] || problem);
    body.append(line);
  }
  const tools = el("div", "msg-tools" + (RETRYABLE.has(message.state) ? " keep" : ""));
  if (message.reply && !active) tools.append(copyButton(() => message.reply));
  if (RETRYABLE.has(message.state) && message === state.messages[state.messages.length - 1]) {
    const retry = el("button");
    retry.type = "button";
    retry.append(icon("retry"), el("span", "", "重试"));
    retry.addEventListener("click", () => retryMessage(message, retry));
    tools.append(retry);
  }
  node.replaceChildren(head, body, tools);
}

function updateMessage(message) {
  if (!message.node || !message.node.isConnected) return;
  if (message.frame) return;
  message.frame = requestAnimationFrame(() => {
    message.frame = 0;
    const stick = nearBottom();
    fillAssistant(message, message.node);
    if (stick) scrollToBottom(false);
  });
}

function nearBottom() { return ui.thread.scrollHeight - ui.thread.scrollTop - ui.thread.clientHeight < 120; }
function scrollToBottom(instant) {
  if (instant) ui.thread.style.scrollBehavior = "auto";
  ui.thread.scrollTop = ui.thread.scrollHeight;
  if (instant) ui.thread.style.scrollBehavior = "";
}

async function retryMessage(message, button) {
  button.disabled = true;
  try {
    const meta = await request(`/sessions/${state.currentId}/messages/${message.id}/retry`, { method: "POST" });
    message.state = meta.state;
    message.error = meta.error || "";
    updateMessage(message);
  } catch (error) {
    report(error);
    button.disabled = false;
  }
}

/* ---------- shared draft and live recording ---------- */

async function loadDraft() {
  const id = state.currentId;
  if (!id || state.sending) return; // send() reloads the draft once it settles.
  const sequence = ++state.draft.sequence;
  const result = await request(`/sessions/${id}/draft/content`, { raw: true });
  if (id !== state.currentId || sequence !== state.draft.sequence) return;
  const text = result.data;
  state.draft.revision = Number(result.headers.get("X-Revision")) || 0;
  const previous = state.draft.text;
  state.draft.text = text;
  if (!state.draft.dirty || ui.input.value === text) {
    ui.input.value = text;
    state.draft.dirty = false;
  }
  if (text && text !== previous && state.draft.dirty) toast("设备更新了草稿；发送时将以输入框中的内容为准。");
  composerChanged();
}

function trackLive(event) {
  if (event.status === "capturing" && event.segment_id) {
    if (event.segment_id !== state.live.segment || ui.live.hidden) {
      state.live.segment = event.segment_id;
      setLive("设备正在录音…", true); // Interim events keep the last fetched text.
    }
    fetchLiveText();
  } else if (event.segment_id && event.segment_id === state.live.segment) {
    if (event.status === "staged" || event.status === "ready") setLive("转写完成，正在保存…", false);
    else setLive(null);
    clearTimeout(state.live.timer);
    state.live.timer = setTimeout(() => setLive(null), 2500);
    return;
  }
  clearTimeout(state.live.timer);
  state.live.timer = setTimeout(() => setLive(null), 12000);
}

async function fetchLiveText() {
  if (!state.live.segment) return;
  if (state.live.fetching) { state.live.again = true; return; }
  state.live.fetching = true;
  state.live.again = false;
  const id = state.currentId;
  const segment = state.live.segment;
  try {
    const win = await request(`/sessions/${id}/segments/${segment}/text?tail=1&limit=600`);
    if (id === state.currentId && segment === state.live.segment && !ui.live.hidden && win.text) {
      setLive("设备正在录音：" + win.text, true);
    }
  } catch { /* the segment may already be retired */ }
  finally {
    await sleep(350);
    state.live.fetching = false;
    if (state.live.again) fetchLiveText();
  }
}

function setLive(text, recording) {
  if (!text) {
    ui.live.hidden = true;
    state.live.segment = "";
    return;
  }
  ui["live-text"].textContent = text;
  ui.live.classList.toggle("rec", !!recording);
  ui.live.hidden = false;
}

/* ---------- composer and sending ---------- */

function lastMessageActive() {
  const last = state.messages[state.messages.length - 1];
  return !!last && ACTIVE.has(last.state);
}

function composerChanged() {
  const area = ui.input;
  area.style.height = "auto";
  area.style.height = Math.min(area.scrollHeight, Math.round(window.innerHeight * 0.4)) + "px";
  state.draft.dirty = area.value !== state.draft.text;
  const hasText = area.value.trim().length > 0;
  const session = currentSession();
  const blocked = state.sending || lastMessageActive() || (session && session.state === "deleting");
  ui.send.disabled = !hasText || blocked;
  ui.send.title = lastMessageActive() ? "等待当前回复完成" : "发送";
  let note = "";
  if (state.draft.text && !state.draft.dirty) note = "来自共享草稿（设备语音）";
  else if (state.draft.text && state.draft.dirty) note = "已修改共享草稿，发送时将替换";
  ui["draft-note"].textContent = note;
  ui["draft-note"].hidden = !note;
}

async function draftItems(id) {
  for (let attempt = 0; attempt < 3; attempt++) {
    const items = [];
    let revision = 0;
    let consistent = true;
    for (let offset = 0; ; offset += PAGE) {
      const page = await request(`/sessions/${id}/draft?limit=${PAGE}&offset=${offset}`);
      if (offset === 0) revision = page.revision;
      else if (page.revision !== revision) { consistent = false; break; }
      items.push(...page.items);
      if (!page.has_more) break;
    }
    if (consistent) return { items, revision };
  }
  throw apiError(409, "stale_revision");
}

// Make the shared draft equal the composer text and return its revision.
async function syncDraft(id, text) {
  for (let attempt = 0; attempt < 3; attempt++) {
    const { items, revision } = await draftItems(id);
    if (items.some(item => item.state === "capturing" || item.state === "staged")) {
      throw Object.assign(new Error("设备正在录音，请等它完成后再发送。"), { code: "busy" });
    }
    try {
      const current = await request(`/sessions/${id}/draft/content?revision=${revision}`, { raw: true });
      if (current.data === text) return revision;
      let next = revision;
      if (!items.length) {
        next = (await request(`/sessions/${id}/segments`, { method: "POST", json: { id: uuid(), revision: next, text } })).revision;
      } else {
        next = (await request(`/sessions/${id}/segments/${items[0].id}`, { method: "PATCH", json: { revision: next, text } })).revision;
        for (const item of items.slice(1)) {
          next = (await request(`/sessions/${id}/segments/${item.id}`, { method: "DELETE", json: { revision: next } })).revision;
        }
      }
      return next;
    } catch (error) {
      if (error.code === "stale_revision" && attempt < 2) continue;
      throw error;
    }
  }
  throw apiError(409, "stale_revision");
}

function titleFrom(text) {
  const line = text.split("\n").map(s => s.trim()).find(Boolean) || DEFAULT_TITLE;
  const chars = Array.from(line);
  return chars.length > 24 ? chars.slice(0, 24).join("") + "…" : line;
}

async function send() {
  const text = ui.input.value.trim();
  if (!text || state.sending || lastMessageActive()) return;
  if (byteLength(text) > 64 * 1024) { toast("内容超过 64 KiB 上限。"); return; }
  state.sending = true;
  composerChanged();
  let id = state.currentId;
  try {
    if (!id) {
      id = uuid();
      const created = await request("/sessions", { method: "POST", json: { id, title: titleFrom(text) } });
      state.sessions.unshift(created);
      await openSession(id, { force: true });
      state.localDrafts.delete("");
      ui.input.value = text;
    }
    const revision = await syncDraft(id, text);
    const messageId = uuid();
    const payload = { method: "POST", json: { id: messageId, revision } };
    let meta;
    try {
      meta = await request(`/sessions/${id}/messages`, payload);
    } catch (error) {
      if (!(error instanceof TypeError)) throw error;
      meta = await request(`/sessions/${id}/messages`, payload); // Same ID and revision are idempotent.
    }
    if (id !== state.currentId) return;
    let message = findMessage(meta.id);
    if (!message) {
      message = makeMessage(meta);
      message.input = text;
      state.messages.push(message);
    }
    state.localDrafts.delete(id);
    state.draft.text = "";
    state.draft.dirty = false;
    ui.input.value = "";
    renderThread();
    scrollToBottom(true);
    const session = currentSession();
    if (session && session.title === DEFAULT_TITLE) {
      request(`/sessions/${id}`, { method: "PATCH", json: { title: titleFrom(text) } })
        .then(updated => { Object.assign(session, updated); renderSessions(); syncHeader(); })
        .catch(() => {});
    }
    refreshSessionsSoon();
  } catch (error) {
    if (error.code === "stale_revision") loadDraft().catch(() => {});
    report(error);
  } finally {
    state.sending = false;
    composerChanged();
    if (state.currentId) loadDraft().catch(() => {});
  }
}

/* ---------- rename and delete ---------- */

let renameTarget = null;
function openRename(session) {
  renameTarget = session;
  ui["rename-input"].value = session.title || "";
  ui["rename-dialog"].showModal();
  ui["rename-input"].select();
}

async function confirmRename(event) {
  event.preventDefault();
  const target = renameTarget;
  const title = ui["rename-input"].value.trim();
  if (!target || !title) return;
  if (title === target.title) { ui["rename-dialog"].close(); return; }
  ui["rename-confirm"].disabled = true;
  try {
    const updated = await request(`/sessions/${target.id}`, { method: "PATCH", json: { title } });
    Object.assign(target, updated);
    ui["rename-dialog"].close();
    renderSessions();
    syncHeader();
  } catch (error) {
    report(error);
  } finally {
    ui["rename-confirm"].disabled = false;
  }
}

let deleteTarget = null;
function openDelete(session) {
  deleteTarget = session;
  ui["delete-name"].textContent = session.title || "未命名会话";
  ui["delete-dialog"].showModal();
}

async function confirmDelete() {
  const target = deleteTarget;
  if (!target) return;
  ui["delete-confirm"].disabled = ui["delete-cancel"].disabled = true;
  ui["delete-confirm"].textContent = "正在删除…";
  try {
    const result = await request(`/sessions/${target.id}`, { method: "DELETE" });
    ui["delete-dialog"].close();
    if (result.state === "deleting") {
      target.state = "deleting";
      toast("已提交删除，正在等待 Hermes 同步。");
    } else {
      state.sessions = state.sessions.filter(s => s.id !== target.id);
      toast("会话已删除。");
    }
    if (target.id === state.currentId) showWelcome();
    renderSessions();
    syncHeader();
    refreshSessionsSoon();
  } catch (error) {
    ui["delete-dialog"].close();
    report(error);
  } finally {
    deleteTarget = null;
    ui["delete-confirm"].disabled = ui["delete-cancel"].disabled = false;
    ui["delete-confirm"].textContent = "删除";
  }
}

/* ---------- device diagnostic reports ---------- */

async function openLogs() {
  if (!ui["logs-dialog"].open) ui["logs-dialog"].showModal();
  ui["logs-back"].hidden = true;
  ui["logs-text"].hidden = true;
  ui["logs-list"].hidden = false;
  ui["logs-note"].hidden = false;
  ui["logs-title"].textContent = "设备诊断日志";
  ui["logs-list"].replaceChildren(el("p", "muted", "正在载入…"));
  try {
    const page = await request(`/device/logs?limit=${PAGE}`);
    const fragment = document.createDocumentFragment();
    for (const item of page.items) {
      const row = el("button", "log-row");
      row.type = "button";
      row.append(el("strong", "", `${fullFormat.format(item.received_at)} · ${item.summary}`),
        el("span", "", `固件 ${item.firmware} · 本次复位：${item.reset_reason}`));
      row.addEventListener("click", () => showLog(item));
      fragment.append(row);
    }
    if (!page.items.length) fragment.append(el("p", "muted", "还没有设备上报的诊断日志。"));
    ui["logs-list"].replaceChildren(fragment);
  } catch (error) {
    ui["logs-list"].replaceChildren(el("p", "state-line error", "读取失败"));
    report(error);
  }
}

async function showLog(item) {
  ui["logs-list"].hidden = true;
  ui["logs-note"].hidden = true;
  ui["logs-back"].hidden = false;
  ui["logs-text"].hidden = false;
  ui["logs-title"].textContent = fullFormat.format(item.received_at);
  ui["logs-text"].textContent = "正在载入…";
  try {
    ui["logs-text"].textContent = await request(`/device/logs/${item.id}`);
  } catch (error) {
    ui["logs-text"].textContent = "读取失败";
    report(error);
  }
}

/* ---------- Markdown (DOM-built, never innerHTML) ---------- */

const FENCE = /^ {0,3}(`{3,}|~{3,})\s*([^`\s]*)/;
const HEADING = /^ {0,3}(#{1,6})\s+(.*?)\s*#*\s*$/;
const RULE = /^ {0,3}([-*_])(?:\s*\1){2,}\s*$/;
const QUOTE = /^ {0,3}>\s?/;
const LIST = /^(\s*)([-*+]|\d{1,9}[.)])\s+(.*)$/;
const TABLE_SEPARATOR = /^\s*\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?\s*$/;

function renderMarkdown(source) {
  const fragment = document.createDocumentFragment();
  blocks(source.replace(/\r\n?/g, "\n").split("\n"), fragment);
  return fragment;
}

function startsBlock(lines, i) {
  const line = lines[i];
  return FENCE.test(line) || HEADING.test(line) || RULE.test(line) || QUOTE.test(line) || LIST.test(line) ||
    (line.includes("|") && i + 1 < lines.length && TABLE_SEPARATOR.test(lines[i + 1]) && lines[i + 1].includes("-"));
}

function blocks(lines, parent) {
  let i = 0;
  while (i < lines.length) {
    const line = lines[i];
    if (!line.trim()) { i++; continue; }
    let m;
    if ((m = line.match(FENCE))) {
      const fence = m[1];
      const close = new RegExp("^ {0,3}" + (fence[0] === "`" ? "`" : "~") + "{" + fence.length + ",}\\s*$");
      const body = [];
      i++;
      while (i < lines.length && !close.test(lines[i])) { body.push(lines[i]); i++; }
      i++; // An unclosed fence (a reply still streaming) runs to the end.
      parent.append(codeBlock(body.join("\n"), m[2]));
      continue;
    }
    if ((m = line.match(HEADING))) {
      const heading = el("h" + m[1].length);
      inline(m[2], heading);
      parent.append(heading);
      i++;
      continue;
    }
    if (RULE.test(line)) { parent.append(el("hr")); i++; continue; }
    if (QUOTE.test(line)) {
      const inner = [];
      while (i < lines.length && lines[i].trim() && QUOTE.test(lines[i])) { inner.push(lines[i].replace(QUOTE, "")); i++; }
      const quote = el("blockquote");
      blocks(inner, quote);
      parent.append(quote);
      continue;
    }
    if (line.includes("|") && i + 1 < lines.length && TABLE_SEPARATOR.test(lines[i + 1]) && lines[i + 1].includes("-")) {
      i = table(lines, i, parent);
      continue;
    }
    if (LIST.test(line)) { i = list(lines, i, parent); continue; }
    const paragraph = [line.trim()];
    i++;
    while (i < lines.length && lines[i].trim() && !startsBlock(lines, i)) { paragraph.push(lines[i].trim()); i++; }
    const p = el("p");
    inline(paragraph.join("\n"), p);
    parent.append(p);
  }
}

function codeBlock(code, language) {
  const wrap = el("div", "code");
  const head = el("div", "code-head");
  head.append(el("span", "", language || "代码"), copyButton(() => code, "复制代码", "复制"));
  const pre = el("pre");
  pre.append(el("code", "", code));
  wrap.append(head, pre);
  return wrap;
}

function cells(line) {
  let text = line.trim();
  if (text.startsWith("|")) text = text.slice(1);
  if (text.endsWith("|") && !text.endsWith("\\|")) text = text.slice(0, -1);
  return text.split(/(?<!\\)\|/).map(cell => cell.trim().replace(/\\\|/g, "|"));
}

function table(lines, i, parent) {
  const header = cells(lines[i]);
  const align = cells(lines[i + 1]).map(c => c.startsWith(":") && c.endsWith(":") ? "center" : c.endsWith(":") ? "right" : "");
  const tableNode = el("table");
  const head = el("thead");
  const headRow = el("tr");
  header.forEach((cell, index) => {
    const th = el("th");
    if (align[index]) th.style.textAlign = align[index];
    inline(cell, th);
    headRow.append(th);
  });
  head.append(headRow);
  const body = el("tbody");
  i += 2;
  while (i < lines.length && lines[i].trim() && lines[i].includes("|")) {
    const tr = el("tr");
    const row = cells(lines[i]);
    header.forEach((_, index) => {
      const td = el("td");
      if (align[index]) td.style.textAlign = align[index];
      inline(row[index] || "", td);
      tr.append(td);
    });
    body.append(tr);
    i++;
  }
  tableNode.append(head, body);
  const wrap = el("div", "table-wrap");
  wrap.append(tableNode);
  parent.append(wrap);
  return i;
}

function indentOf(line) { return line.match(/^\s*/)[0].replace(/\t/g, "    ").length; }

function list(lines, i, parent) {
  const first = lines[i].match(LIST);
  const indent = first[1].length;
  const ordered = /\d/.test(first[2]);
  const node = el(ordered ? "ol" : "ul");
  if (ordered && parseInt(first[2], 10) !== 1) node.start = parseInt(first[2], 10);
  let loose = false;
  while (i < lines.length) {
    const m = lines[i].match(LIST);
    if (!m || m[1].length !== indent || /\d/.test(m[2]) !== ordered) break;
    const content = indent + m[2].length + 1;
    const item = [m[3]];
    i++;
    while (i < lines.length) {
      const line = lines[i];
      if (!line.trim()) {
        let j = i + 1;
        while (j < lines.length && !lines[j].trim()) j++;
        if (j < lines.length && indentOf(lines[j]) > indent) { item.push(""); i++; loose = true; continue; }
        break;
      }
      const child = line.match(LIST);
      if (child && child[1].length <= indent) break;
      const lead = indentOf(line);
      if (lead <= indent && !child) {
        if (item[item.length - 1] === "" || startsBlock(lines, i)) break;
        item.push(line.trim()); // Lazy paragraph continuation.
      } else {
        item.push(line.replace(/^\s+/, s => " ".repeat(Math.max(0, s.length - Math.min(content, s.length)))));
      }
      i++;
    }
    const li = el("li");
    blocks(item, li);
    if (!loose && li.childNodes.length && li.firstChild.tagName === "P") {
      const p = li.firstChild;
      p.replaceWith(...p.childNodes);
    }
    node.append(li);
  }
  parent.append(node);
  return i;
}

const INLINE = /(`+)([^`]|[^`][\s\S]*?[^`])\1(?!`)|\*\*(?=\S)([\s\S]+?)\*\*|__(?=\S)([\s\S]+?)__|~~(?=\S)([\s\S]+?)~~|\*(?=[^\s*])([\s\S]+?)\*|(?<![\p{L}\p{N}])_(?=\S)([\s\S]+?)_(?![\p{L}\p{N}])|\[([^\]\n]+)\]\(\s*<?([^)\s>]+)>?(?:\s+"[^"]*")?\s*\)|<(https?:\/\/[^>\s]+)>|(https?:\/\/[^\s<>"'，。！？、；：（）【】《》「」]+)/gu;

function safeUrl(value) {
  try {
    const url = new URL(value, location.href);
    return ["http:", "https:", "mailto:"].includes(url.protocol) ? url.href : "";
  } catch { return ""; }
}

function link(href, parent, label) {
  const url = safeUrl(href);
  if (!url) { parent.append(document.createTextNode(label || href)); return; }
  const a = el("a");
  a.href = url;
  a.target = "_blank";
  a.rel = "noopener noreferrer";
  if (label === undefined) a.textContent = href;
  else inline(label, a);
  parent.append(a);
}

function plain(text, parent) {
  const parts = text.replace(/\\([\\`*_{}\[\]()#+\-.!|~>])/g, "$1").split("\n");
  parts.forEach((part, index) => {
    if (index) parent.append(el("br"));
    if (part) parent.append(document.createTextNode(part));
  });
}

function inline(text, parent) {
  const pattern = new RegExp(INLINE.source, INLINE.flags);
  let last = 0;
  let m;
  while ((m = pattern.exec(text))) {
    if (m.index > 0 && text[m.index - 1] === "\\") continue;
    let end = m.index + m[0].length;
    if (m[11]) {
      // Keep sentence punctuation out of bare links.
      const trimmed = m[11].replace(/[.,;:!?)\]]+$/, "");
      end = m.index + trimmed.length;
      pattern.lastIndex = end;
      plain(text.slice(last, m.index), parent);
      link(trimmed, parent);
      last = end;
      continue;
    }
    plain(text.slice(last, m.index), parent);
    if (m[1]) {
      const code = m[2];
      parent.append(el("code", "", code.length > 2 && code.startsWith(" ") && code.endsWith(" ") ? code.slice(1, -1) : code));
    } else if (m[3] || m[4]) {
      const strong = el("strong");
      inline(m[3] || m[4], strong);
      parent.append(strong);
    } else if (m[5]) {
      const del = el("del");
      inline(m[5], del);
      parent.append(del);
    } else if (m[6] || m[7]) {
      const em = el("em");
      inline(m[6] || m[7], em);
      parent.append(em);
    } else if (m[8]) {
      link(m[9], parent, m[8]);
    } else if (m[10]) {
      link(m[10], parent);
    }
    last = end;
  }
  plain(text.slice(last), parent);
}

/* ---------- sidebar (narrow screens) ---------- */

function openSidebar() { ui.app.classList.add("side-open"); ui.scrim.hidden = false; }
function closeSidebar() { ui.app.classList.remove("side-open"); ui.scrim.hidden = true; }

/* ---------- wiring ---------- */

applyTheme(storageGet(THEME_STORE) || "system");
ui.theme.addEventListener("click", () => {
  const current = document.documentElement.dataset.theme || "system";
  const next = THEMES[(THEMES.indexOf(current) + 1) % THEMES.length];
  storageSet(THEME_STORE, next === "system" ? "" : next);
  applyTheme(next);
  toast("主题：" + THEME_LABEL[next]);
});

ui["auth-form"].addEventListener("submit", async event => {
  event.preventDefault();
  const key = ui["api-key"].value.trim();
  if (!key) return;
  state.key = key;
  ui["auth-error"].hidden = true;
  const ok = await connect();
  if (ok) {
    if (ui.remember.checked) storageSet(KEY_STORE, key);
    ui["api-key"].value = "";
  } else if (!state.authed) {
    state.key = "";
    ui["auth-error"].textContent = "访问密钥无效，请重新输入。";
    ui["auth-error"].hidden = false;
  }
});
ui["sign-out"].addEventListener("click", () => {
  state.sessions = [];
  renderSessions();
  showWelcome();
  requireKey("");
});

ui["device-logs"].addEventListener("click", () => { if (state.authed) openLogs(); });
ui["logs-back"].addEventListener("click", openLogs);
ui["logs-close"].addEventListener("click", () => ui["logs-dialog"].close());
ui["new-chat"].addEventListener("click", () => { showWelcome(); closeSidebar(); ui.input.focus(); });
ui.search.addEventListener("input", renderSessions);
ui["session-list"].addEventListener("scroll", () => {
  const list = ui["session-list"];
  if (!ui.search.value && list.scrollHeight - list.scrollTop - list.clientHeight < 60) loadMoreSessions();
});
ui["open-sidebar"].addEventListener("click", openSidebar);
ui["close-sidebar"].addEventListener("click", closeSidebar);
ui.scrim.addEventListener("click", closeSidebar);
ui.rename.addEventListener("click", () => { const s = currentSession(); if (s) openRename(s); });
ui.delete.addEventListener("click", () => { const s = currentSession(); if (s) openDelete(s); });
ui["rename-form"].addEventListener("submit", confirmRename);
ui["rename-cancel"].addEventListener("click", () => ui["rename-dialog"].close());
ui["delete-cancel"].addEventListener("click", () => ui["delete-dialog"].close());
ui["delete-confirm"].addEventListener("click", confirmDelete);
ui.thread.addEventListener("scroll", () => {
  document.querySelector(".topbar").classList.toggle("scrolled", ui.thread.scrollTop > 4);
});

ui.input.addEventListener("input", composerChanged);
ui.input.addEventListener("keydown", event => {
  if (event.key === "Enter" && !event.shiftKey && !event.isComposing && event.keyCode !== 229) {
    event.preventDefault();
    send();
  }
});
ui.composer.addEventListener("submit", event => { event.preventDefault(); send(); });

document.addEventListener("keydown", event => {
  if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "k") {
    event.preventDefault();
    if (state.authed) { showWelcome(); ui.input.focus(); }
  } else if (event.key === "Escape" && ui.app.classList.contains("side-open")) {
    closeSidebar();
  }
});
window.addEventListener("hashchange", () => {
  const id = routeId();
  if (!state.authed) return;
  if (id && id !== state.currentId) openSession(id, { fromRoute: true });
  else if (!id && state.currentId) showWelcome();
});
window.addEventListener("resize", () => composerChanged());

state.key = storageGet(KEY_STORE);
ui.remember.checked = !!state.key;
renderSessions();
showWelcome({ keepRoute: true }); // Keep a #/c/<id> link until connect() opens it.
connect();
