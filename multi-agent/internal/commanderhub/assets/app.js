// commander /commander page. All API calls use the observer's httpOnly session
// cookie (credentials:'include'). No token is ever stored in JS.
const app = document.getElementById("app");
const auth = document.getElementById("auth");

let viewGeneration = 0;

function beginView() {
  viewGeneration += 1;
  return viewGeneration;
}

function isCurrentView(view) {
  return view === viewGeneration;
}

async function api(path, opts = {}) {
  const res = await fetch(path, { credentials: "include", ...opts });
  if (res.status === 401) { showLogin(); throw new Error("unauthorized"); }
  return res;
}

function showLogin() {
  beginView();
  auth.innerHTML = "";
  const btn = document.createElement("button");
  btn.textContent = "用 agentserver 登录";
  btn.onclick = startLogin;
  auth.appendChild(btn);
  app.innerHTML = '<p class="muted">请先登录。</p>';
}

async function startLogin() {
  const view = beginView();
  auth.innerHTML = '<button disabled>用 agentserver 登录…</button>';
  app.innerHTML = '<p>正在向 agentserver 请求登录码…</p>';
  let r;
  try {
    r = await fetch("/api/commander/login", { method: "POST", credentials: "include" });
  } catch (e) { app.innerHTML = '<p class="muted">请求失败,请重试。</p>'; showLogin(); return; }
  if (!isCurrentView(view)) return;
  if (!r.ok) { app.innerHTML = '<p class="muted">请求登录失败 (HTTP ' + r.status + '),请稍后重试。</p>'; showLogin(); return; }
  const { verification_uri_complete, login_id } = await r.json();
  if (!isCurrentView(view)) return;

  // Show the authorize URL on the page (popup-blocker safe) + a status line.
  // Link built with createElement/textContent so the agentserver URL can't inject markup.
  app.innerHTML = "";
  const p1 = document.createElement("p");
  p1.textContent = "1. 打开下面链接并在 agentserver 授权(应已自动弹出新标签;若被拦截,点此链接):";
  const a = document.createElement("a");
  a.href = verification_uri_complete; a.target = "_blank"; a.textContent = verification_uri_complete;
  const p2 = document.createElement("p"); p2.appendChild(a);
  const p3 = document.createElement("p"); p3.className = "muted";
  p3.textContent = "2. 授权后回到本页,自动登录 → ";
  const status = document.createElement("span"); status.id = "loginstatus"; status.textContent = "等待授权…";
  p3.appendChild(status);
  app.appendChild(p1); app.appendChild(p2); app.appendChild(p3);
  try { window.open(verification_uri_complete, "_blank"); } catch (e) { /* link above is the fallback */ }

  // Raw fetch (not api()) so a 401/404 during polling just returns to the login
  // button instead of throwing through api()'s handler.
  const poll = async () => {
    if (!isCurrentView(view)) return;
    let pr;
    try {
      pr = await fetch("/api/commander/login/poll?id=" + encodeURIComponent(login_id), { credentials: "include" });
    } catch (e) { setTimeout(poll, 1500); return; }
    if (!isCurrentView(view)) return;
    let body = {};
    try { body = await pr.json(); } catch (e) {}
    if (!isCurrentView(view)) return;
    if (pr.status === 401 || pr.status === 404) {
      auth.innerHTML = "";
      const retry = document.createElement("button");
      retry.textContent = "重新登录";
      retry.onclick = startLogin;
      auth.appendChild(retry);
      const msg = document.createElement("p");
      msg.className = "muted";
      msg.textContent = body.error || "登录失败或已过期。";
      app.innerHTML = "";
      app.appendChild(msg);
      return;
    } // failed/expired/gone
    if (body.status === "ok") { await whoami(); return; }
    const s = document.getElementById("loginstatus"); if (s) s.textContent = "等待授权…";
    setTimeout(poll, 1500);
  };
  poll();
}

async function whoami() {
  // After login the cookie is set; load the daemon list to confirm + render.
  auth.innerHTML = '<button id="logout">登出</button>';
  document.getElementById("logout").onclick = async () => {
    await api("/api/commander/logout", { method: "POST" }); showLogin();
  };
  await showDaemons();
}

async function showDaemons() {
  const view = beginView();
  const r = await api("/api/commander/daemons");
  if (!isCurrentView(view)) return;
  const { daemons } = await r.json();
  if (!isCurrentView(view)) return;
  app.innerHTML = '<h2>daemons</h2><ul class="daemons"></ul>';
  const ul = app.querySelector("ul.daemons");
  if (!daemons || daemons.length === 0) {
    ul.innerHTML = '<li class="muted">没有在线 daemon(先在机器上跑 driver-agent serve-daemon)。</li>';
    return;
  }
  for (const d of daemons) {
    const li = document.createElement("li");
    li.textContent = `${d.display_name || d.daemon_id} (${d.kind})`;
    li.onclick = () => showSessions(d.daemon_id, d.display_name);
    ul.appendChild(li);
  }
}

async function showSessions(daemonID, name) {
  const view = beginView();
  const r = await api(`/api/commander/daemons/${daemonID}/sessions`);
  if (!isCurrentView(view)) return;
  const { sessions } = await r.json();
  if (!isCurrentView(view)) return;
  // Static shell only (no untrusted interpolation); the daemon-controlled
  // display_name is set via textContent below to avoid DOM XSS.
  app.innerHTML = `<h2><span class="daemon-name"></span> · sessions</h2><button id="back">← daemons</button><ul class="sessions"></ul>`;
  app.querySelector("h2 .daemon-name").textContent = name || daemonID;
  document.getElementById("back").onclick = showDaemons;
  const ul = app.querySelector("ul.sessions");
  if (!sessions || sessions.length === 0) {
    ul.innerHTML = '<li class="muted">这台 daemon 暂无 session。</li>'; return;
  }
  for (const s of sessions) {
    const li = document.createElement("li");
    li.textContent = `${s.ID}  ${(s.Preview || "").slice(0, 60)}`;
    li.onclick = () => showChat(daemonID, s.ID);
    ul.appendChild(li);
  }
}

async function showChat(daemonID, sid) {
  const view = beginView();
  const r = await api(`/api/commander/daemons/${daemonID}/sessions/${sid}`);
  if (!isCurrentView(view)) return;
  const { session, messages } = await r.json();
  if (!isCurrentView(view)) return;
  // Static shell only (no untrusted interpolation); the session-controlled ID
  // is set via textContent below to avoid DOM XSS.
  app.innerHTML = `<h2><span class="session-id"></span></h2>
    <button id="back">← sessions</button>
    <div class="chat" id="chat"></div>
    <textarea id="prompt" rows="2" placeholder="发一轮 turn…"></textarea>
    <button id="send">发送</button>`;
  app.querySelector("h2 .session-id").textContent = (session && session.ID) || sid;
  document.getElementById("back").onclick = () => showSessions(daemonID, session && session.WorkingDir || "");
  const chat = document.getElementById("chat");
  (messages || []).forEach(renderMsg);
  document.getElementById("send").onclick = () => sendTurn(daemonID, sid);

  function renderMsg(m) {
    const div = document.createElement("div");
    div.className = "msg " + (m.Role || "");
    div.textContent = m.Text || "";
    chat.appendChild(div);
  }
  chat.scrollTop = chat.scrollHeight;
}

async function sendTurn(daemonID, sid) {
  const prompt = document.getElementById("prompt").value.trim();
  if (!prompt) return;
  const chat = document.getElementById("chat");
  const div = document.createElement("div");
  div.className = "msg user"; div.textContent = prompt;
  chat.appendChild(div);
  const assistant = document.createElement("div");
  assistant.className = "msg assistant"; chat.appendChild(assistant);
  document.getElementById("prompt").value = "";

  // POST + stream SSE via fetch ReadableStream (EventSource can't POST).
  const res = await fetch(`/api/commander/daemons/${daemonID}/sessions/${sid}/turn`, {
    method: "POST",
    credentials: "include",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ prompt }),
  });
  if (!res.ok) { assistant.textContent = "错误: " + res.status; return; }
  const reader = res.body.getReader();
  const dec = new TextDecoder();
  let buf = "";
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    buf += dec.decode(value, { stream: true });
    let idx;
    while ((idx = buf.indexOf("\n\n")) >= 0) {
      const block = buf.slice(0, idx); buf = buf.slice(idx + 2);
      handleSSE(block, assistant);
    }
  }
  chat.scrollTop = chat.scrollHeight;
}

function handleSSE(block, assistant) {
  let event = "message";
  for (const line of block.split("\n")) {
    if (line.startsWith("event: ")) event = line.slice(7);
    else if (line.startsWith("data: ")) {
      let text = "";
      let body = null;
      try {
        body = JSON.parse(line.slice(6));
        text = body.text || "";
        if (event === "error") text = body.message || body.code || text;
        if (event === "done" && body.result && body.result.summary) text = body.result.summary;
      } catch {
        if (event === "error") text = line.slice(6);
      }
      if (event === "status" && text && !assistant._hasChunk) {
        assistant.textContent = text;
        assistant._statusOnly = true;
      }
      if (event === "chunk" && text) {
        if (assistant._statusOnly) assistant.textContent = "";
        assistant._statusOnly = false;
        assistant._hasChunk = true;
        assistant.textContent += text;
      }
      if (event === "error") {
        assistant.textContent = "错误: " + (text || "turn failed");
        assistant._statusOnly = false;
      }
      if (event === "done") {
        if (assistant._statusOnly && text) {
          assistant.textContent = text;
          assistant._statusOnly = false;
        }
        try { const r = body || JSON.parse(line.slice(6)); if (r.result && r.result.awaiting_user) {
          assistant.textContent += "\n(需审批:请到 CLI 端继续)";
        } } catch {}
      }
    }
  }
}

// boot: try /daemons to see if already authed (cookie present); else show login.
(async function boot() {
  try {
    const r = await fetch("/api/commander/daemons", { credentials: "include" });
    if (r.ok) { await whoami(); } else { showLogin(); }
  } catch { showLogin(); }
})();
