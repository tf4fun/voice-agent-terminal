"use strict";

const ui = {};
for (const id of [
  "connection", "refresh", "sign-out", "notice", "authentication", "auth-form", "api-key",
  "workspace", "list-status", "sessions", "list-empty", "previous-page", "next-page", "page-number",
  "detail-empty", "detail", "session-title", "session-meta", "back-to-list", "delete-session",
  "older-messages", "newer-messages", "conversation-status", "conversation", "delete-dialog",
  "delete-name", "cancel-delete", "confirm-delete",
]) {
  ui[id] = document.getElementById(id);
}

const state = {
  key: "", offset: 0, hasMore: false, sessions: [], selected: null,
  listSequence: 0, detailSequence: 0, listController: null, detailController: null,
  before: "", after: "", deletionTimer: null, deleteTarget: null, deleting: false,
};
const pageSize = 20;
const dateFormat = new Intl.DateTimeFormat("zh-CN", { dateStyle: "medium", timeStyle: "short" });

function notice(text) {
  ui.notice.textContent = text;
  ui.notice.hidden = !text;
}

function dateText(timestamp) {
  return timestamp > 0 ? dateFormat.format(new Date(timestamp)) : "暂无时间记录";
}

function sessionState(session) {
  if (session.state === "deleting") return "删除中，等待 Hermes 同步";
  if (session.state === "creating") return session.error ? "等待 Hermes 连接" : "正在建立会话";
  return "";
}

function clearDetail() {
  state.detailSequence++;
  state.detailController?.abort();
  state.selected = null;
  state.before = state.after = "";
  ui.detail.hidden = true;
  ui["detail-empty"].hidden = false;
  ui.workspace.classList.remove("is-detail");
  ui.conversation.replaceChildren();
}

function requireKey() {
  state.key = "";
  state.listSequence++;
  state.listController?.abort();
  clearTimeout(state.deletionTimer);
  clearDetail();
  state.sessions = [];
  ui.sessions.replaceChildren();
  ui.workspace.hidden = true;
  ui.authentication.hidden = false;
  ui.refresh.hidden = true;
  ui["sign-out"].hidden = true;
  ui.connection.textContent = "需要访问密钥";
  if (ui["delete-dialog"].open) ui["delete-dialog"].close();
  ui["api-key"].focus();
}

async function request(path, options = {}) {
  const suppliedKey = state.key;
  const headers = new Headers({ Accept: "application/json" });
  if (suppliedKey) headers.set("Authorization", "Bearer " + suppliedKey);
  const response = await fetch("/api/v1" + path, { ...options, headers, cache: "no-store" });
  const data = response.headers.get("Content-Type")?.includes("application/json")
    ? await response.json() : await response.text();
  if (!response.ok) {
    if (response.status === 401) {
      requireKey();
      const error = new Error(suppliedKey ? "访问密钥无效，请重新输入。" : "请输入访问密钥。");
      error.status = 401;
      if (!suppliedKey) error.name = "AbortError";
      throw error;
    }
    const code = data.error?.code;
    const messages = {
      busy: "会话仍有进行中或结果未确认的消息，请处理后再删除。",
      not_found: "该会话已被删除，请刷新列表。",
      hermes_session_error: "Hermes 暂时无法同步，请稍后重试。",
      stale_revision: "对话已更新，请刷新后继续查看。",
    };
    const error = new Error(messages[code] || `请求失败（${response.status}），请稍后重试。`);
    error.status = response.status;
    throw error;
  }
  return data;
}

function showError(error) {
  if (error.name === "AbortError") return;
  notice(error instanceof TypeError ? "连接失败，请检查服务是否运行。" : error.message || "连接失败，请刷新重试。");
  if (error.status !== 401) ui.connection.textContent = "读取失败";
}

function updateDetailHeader() {
  const session = state.selected;
  if (!session) return;
  ui["session-title"].textContent = session.title || "未命名会话";
  ui["session-meta"].textContent = `更新于 ${dateText(session.updated_at)}${sessionState(session) ? " · " + sessionState(session) : ""}`;
  ui["delete-session"].disabled = state.deleting || session.state === "deleting";
}

function renderSessions() {
  const fragment = document.createDocumentFragment();
  for (const session of state.sessions) {
    const button = document.createElement("button");
    button.type = "button";
    button.className = "session-row";
    button.setAttribute("aria-pressed", String(state.selected?.id === session.id));
    const title = document.createElement("span");
    title.className = "session-title";
    title.textContent = session.title || "未命名会话";
    title.title = title.textContent;
    const date = document.createElement("span");
    date.className = "session-date";
    date.textContent = dateText(session.updated_at);
    button.append(title, date);
    const status = sessionState(session);
    if (status) {
      const label = document.createElement("span");
      label.className = "session-state";
      label.textContent = status;
      button.append(label);
    }
    button.addEventListener("click", () => selectSession(session));
    fragment.append(button);
  }
  ui.sessions.replaceChildren(fragment);
  ui["list-empty"].hidden = state.sessions.length > 0;
  ui.sessions.hidden = state.sessions.length === 0;
  ui["previous-page"].disabled = state.offset === 0;
  ui["next-page"].disabled = !state.hasMore;
  ui["page-number"].textContent = `第 ${state.offset / pageSize + 1} 页`;
  ui["list-status"].textContent = state.sessions.length ? `${state.sessions.length} 条` : "";
}

async function loadSessions() {
  const sequence = ++state.listSequence;
  state.listController?.abort();
  const controller = new AbortController();
  state.listController = controller;
  clearTimeout(state.deletionTimer);
  ui.refresh.disabled = true;
  ui["list-status"].textContent = "读取中";
  try {
    const page = await request(`/sessions?limit=${pageSize}&offset=${state.offset}`, { signal: controller.signal });
    if (sequence !== state.listSequence) return;
    if (!page.items.length && state.offset > 0) {
      state.offset = Math.max(0, state.offset - pageSize);
      return loadSessions();
    }
    state.sessions = page.items;
    state.hasMore = page.has_more;
    if (state.selected) {
      const current = state.sessions.find(session => session.id === state.selected.id);
      if (current) { state.selected = current; updateDetailHeader(); }
      else clearDetail();
    }
    renderSessions();
    ui.workspace.hidden = false;
    ui.authentication.hidden = true;
    ui.refresh.hidden = false;
    ui["sign-out"].hidden = !state.key;
    ui.connection.textContent = "已连接";
    if (state.sessions.some(session => session.state === "deleting")) {
      state.deletionTimer = setTimeout(loadSessions, 2000);
    }
  } catch (error) {
    if (sequence === state.listSequence || error.status === 401) {
      ui["list-status"].textContent = "读取失败";
      showError(error);
    }
  } finally {
    if (sequence === state.listSequence) ui.refresh.disabled = false;
  }
}

function selectSession(session) {
  state.selected = session;
  ui["detail-empty"].hidden = true;
  ui.detail.hidden = false;
  ui.workspace.classList.add("is-detail");
  updateDetailHeader();
  renderSessions();
  ui.conversation.replaceChildren();
  loadConversation();
}

function emptyReply(stateName) {
  if (["queued", "sending", "waiting"].includes(stateName)) return "正在等待回复…";
  if (stateName === "receiving") return "正在回复…";
  if (stateName === "attention") return "等待审批";
  if (["failed", "unconfirmed", "incomplete"].includes(stateName)) return "回复未完成";
  return "暂无文字回复";
}

function renderConversation(items) {
  const fragment = document.createDocumentFragment();
  for (const group of items) {
    const article = document.createElement("article");
    article.className = "message" + (group.part === "input" ? " input" : "");
    const role = document.createElement("p");
    role.className = "message-role";
    role.textContent = group.part === "input" ? "我" : "助手";
    const text = document.createElement("p");
    text.className = "message-text";
    text.textContent = group.text || emptyReply(group.state);
    article.append(role, text);
    fragment.append(article);
  }
  if (!items.length) {
    const empty = document.createElement("p");
    empty.className = "empty-state muted";
    empty.textContent = "这个会话尚无已发送消息。";
    fragment.append(empty);
  }
  ui.conversation.replaceChildren(fragment);
  ui.conversation.scrollTop = 0;
}

async function loadConversation(direction, cursor) {
  if (!state.selected) return;
  const id = state.selected.id;
  const sequence = ++state.detailSequence;
  state.detailController?.abort();
  const controller = new AbortController();
  state.detailController = controller;
  ui["conversation-status"].textContent = "读取中";
  ui["older-messages"].disabled = ui["newer-messages"].disabled = true;
  try {
    const query = direction ? `?${direction}=${encodeURIComponent(cursor)}` : "";
    const page = await request(`/sessions/${id}/turn${query}`, { signal: controller.signal });
    const items = [];
    if (page.message) {
      const message = page.message;
      const [input, reply] = await Promise.all(["input", "reply"].map(part =>
        request(`/sessions/${id}/messages/${message.id}/content?part=${part}`, { signal: controller.signal })));
      items.push({ part: "input", text: input, state: message.state },
        { part: "reply", text: reply, state: message.state });
    }
    if (sequence !== state.detailSequence || state.selected?.id !== id) return;
    state.before = page.before;
    state.after = page.after;
    renderConversation(items);
    ui["conversation-status"].textContent = "";
    ui["older-messages"].disabled = !page.before;
    ui["newer-messages"].disabled = !page.after;
  } catch (error) {
    if (sequence === state.detailSequence) {
      ui["conversation-status"].textContent = error.name === "AbortError" ? "" : "读取失败";
      ui["older-messages"].disabled = !state.before;
      ui["newer-messages"].disabled = !state.after;
      showError(error);
    }
  }
}

function openDeleteDialog() {
  if (!state.selected || state.selected.state === "deleting" || state.deleting) return;
  state.deleteTarget = { id: state.selected.id, title: state.selected.title };
  ui["delete-name"].textContent = state.deleteTarget.title || "未命名会话";
  ui["delete-dialog"].showModal();
}

async function confirmDelete() {
  if (!state.deleteTarget || state.deleting) return;
  const target = state.deleteTarget;
  state.deleting = true;
  ui["confirm-delete"].disabled = true;
  ui["cancel-delete"].disabled = true;
  ui["confirm-delete"].textContent = "正在删除…";
  try {
    const result = await request(`/sessions/${target.id}`, { method: "DELETE" });
    if (ui["delete-dialog"].open) ui["delete-dialog"].close();
    notice(result.state === "deleting" ? "删除请求已提交，正在等待 Hermes 同步。" : "会话已删除。");
    if (result.state !== "deleting" && state.selected?.id === target.id) clearDetail();
    await loadSessions();
  } catch (error) {
    if (ui["delete-dialog"].open) ui["delete-dialog"].close();
    showError(error);
  } finally {
    state.deleting = false;
    state.deleteTarget = null;
    ui["confirm-delete"].disabled = ui["cancel-delete"].disabled = false;
    ui["confirm-delete"].textContent = "确认删除";
    updateDetailHeader();
  }
}

ui["auth-form"].addEventListener("submit", event => {
  event.preventDefault();
  state.key = ui["api-key"].value.trim();
  ui["api-key"].value = "";
  state.offset = 0;
  notice("");
  loadSessions();
});
ui["sign-out"].addEventListener("click", () => { requireKey(); notice(""); });
ui.refresh.addEventListener("click", async () => {
  notice("");
  await loadSessions();
  if (state.selected) loadConversation();
});
for (const [id, direction] of [["previous-page", -1], ["next-page", 1]]) {
  ui[id].addEventListener("click", () => {
    if ((direction < 0 && state.offset === 0) || (direction > 0 && !state.hasMore)) return;
    clearDetail();
    state.offset = Math.max(0, state.offset + direction * pageSize);
    loadSessions();
  });
}
ui["back-to-list"].addEventListener("click", () => { ui.workspace.classList.remove("is-detail"); });
ui["older-messages"].addEventListener("click", () => loadConversation("before", state.before));
ui["newer-messages"].addEventListener("click", () => loadConversation("after", state.after));
ui["delete-session"].addEventListener("click", openDeleteDialog);
ui["cancel-delete"].addEventListener("click", () => ui["delete-dialog"].close());
ui["confirm-delete"].addEventListener("click", confirmDelete);
ui["delete-dialog"].addEventListener("cancel", event => { if (state.deleting) event.preventDefault(); });

loadSessions();
