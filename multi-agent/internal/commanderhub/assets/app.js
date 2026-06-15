// commander /commander page. All API calls use the observer's httpOnly session
// cookie (credentials:'include'). No token is ever stored in JS.
const app = document.getElementById("app");
const auth = document.getElementById("auth");

async function api(path, opts = {}) {
  const res = await fetch(path, { credentials: "include", ...opts });
  if (res.status === 401) { showLogin(); throw new Error("unauthorized"); }
  return res;
}

function showLogin() {
  auth.innerHTML = "";
  const btn = document.createElement("button");
  btn.textContent = "用 agentserver 登录";
  btn.onclick = startLogin;
  auth.appendChild(btn);
  app.innerHTML = '<p class="muted">请先登录。</p>';
}

async function startLogin() {
  const r = await api("/api/commander/login", { method: "POST" });
  const { verification_uri_complete, login_id } = await r.json();
  window.open(verification_uri_complete, "_blank"); // user approves on agentserver
  const poll = async () => {
    const pr = await api("/api/commander/login/poll?id=" + encodeURIComponent(login_id));
    if (pr.status === 401) { app.innerHTML = '<p class="muted">登录失败,重试。</p>'; return; }
    const body = await pr.json();
    if (body.status === "ok") { await whoami(); return; }
    setTimeout(poll, 1500); // pending → keep polling
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
  const r = await api("/api/commander/daemons");
  const { daemons } = await r.json();
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
  const r = await api(`/api/commander/daemons/${daemonID}/sessions`);
  const { sessions } = await r.json();
  app.innerHTML = `<h2>${name} · sessions</h2><button id="back">← daemons</button><ul class="sessions"></ul>`;
  document.getElementById("back").onclick = showDaemons;
  const ul = app.querySelector("ul.sessions");
  if (!sessions || sessions.length === 0) {
    ul.innerHTML = '<li class="muted">这台 daemon 暂无 session。</li>'; return;
  }
  for (const s of sessions) {
    const li = document.createElement("li");
    li.textContent = `${s.id}  ${(s.last_user_msg || "").slice(0, 60)}`;
    li.onclick = () => showChat(daemonID, s.id);
    ul.appendChild(li);
  }
}

async function showChat(daemonID, sid) {
  const r = await api(`/api/commander/daemons/${daemonID}/sessions/${sid}`);
  const { session, messages } = await r.json();
  app.innerHTML = `<h2>${session && session.id || sid}</h2>
    <button id="back">← sessions</button>
    <div class="chat" id="chat"></div>
    <textarea id="prompt" rows="2" placeholder="发一轮 turn…"></textarea>
    <button id="send">发送</button>`;
  document.getElementById("back").onclick = () => showSessions(daemonID, session && session.working_dir || "");
  const chat = document.getElementById("chat");
  (messages || []).forEach(renderMsg);
  document.getElementById("send").onclick = () => sendTurn(daemonID, sid);

  function renderMsg(m) {
    const div = document.createElement("div");
    div.className = "msg " + (m.role || "");
    div.textContent = m.text || "";
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
      try { text = JSON.parse(line.slice(6)).text || ""; } catch {
        // done/error payloads carry result/error objects, not {text}
        if (event === "error") text = "[error] " + line.slice(6);
      }
      if (event === "chunk" && text) assistant.textContent += text;
      if (event === "done") {
        try { const r = JSON.parse(line.slice(6)); if (r.result && r.result.awaiting_user) {
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
