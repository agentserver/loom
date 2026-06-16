package commanderhub

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWeb_CommanderPageAndAssets(t *testing.T) {
	mux := http.NewServeMux()
	MountWeb(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// /commander → HTML
	resp, err := http.Get(srv.URL + "/commander")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "text/html; charset=utf-8", resp.Header.Get("Content-Type"))
	body := make([]byte, 4096)
	n, _ := resp.Body.Read(body)
	resp.Body.Close()
	require.True(t, strings.Contains(string(body[:n]), "commander"), "index references app.js")

	// assets served
	for _, p := range []string{"/commander/app.js", "/commander/style.css"} {
		resp, err := http.Get(srv.URL + p)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode, p)
		resp.Body.Close()
	}

	// unknown path under /commander/ → 404 (no stray fileserver catch-all)
	resp, err = http.Get(srv.URL + "/commander/nope")
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp.Body.Close()
}

func TestWeb_CommanderAppIgnoresStaleSessionListResponses(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable for commander app race test")
	}
	appJS, err := os.ReadFile("assets/app.js")
	require.NoError(t, err)

	script := `
const vm = require("node:vm");

let appHTML = "";
const elements = new Map();
class Element {
  constructor(id) {
    this.id = id;
    this.children = [];
    this._innerHTML = "";
    this.textContent = "";
    this.className = "";
    this.onclick = null;
    this.value = "";
    this.scrollTop = 0;
    this.scrollHeight = 0;
  }
  set innerHTML(value) {
    this._innerHTML = value;
    if (this.id === "app") appHTML = value;
    if (value.includes('ul class="daemons"')) elements.set("ul.daemons", new Element("ul.daemons"));
    if (value.includes('ul class="sessions"')) elements.set("ul.sessions", new Element("ul.sessions"));
    if (value.includes('class="daemon-name"')) elements.set("h2 .daemon-name", new Element("h2 .daemon-name"));
    if (value.includes('class="session-id"')) elements.set("h2 .session-id", new Element("h2 .session-id"));
    if (value.includes('id="back"')) elements.set("back", new Element("back"));
    if (value.includes('id="chat"')) elements.set("chat", new Element("chat"));
    if (value.includes('id="prompt"')) elements.set("prompt", new Element("prompt"));
    if (value.includes('id="send"')) elements.set("send", new Element("send"));
  }
  get innerHTML() { return this._innerHTML; }
  appendChild(child) { this.children.push(child); return child; }
  querySelector(selector) {
    if (!elements.has(selector)) elements.set(selector, new Element(selector));
    return elements.get(selector);
  }
}
function getElement(id) {
  if (!elements.has(id)) elements.set(id, new Element(id));
  return elements.get(id);
}
function response(body, status = 200) {
  return { ok: status >= 200 && status < 300, status, json: async () => body };
}
const sessionListResolvers = [];
const context = {
  console,
  setTimeout,
  TextDecoder,
  window: { open: () => {} },
  document: {
    getElementById: getElement,
    createElement: tag => new Element(tag),
  },
  fetch: async path => {
    if (path === "/api/commander/daemons") return response({}, 401);
    if (path === "/api/commander/daemons/d/sessions") {
      return new Promise(resolve => sessionListResolvers.push(resolve));
    }
    if (path === "/api/commander/daemons/d/sessions/sid") {
      return response({ session: { ID: "sid" }, messages: [] });
    }
    throw new Error("unexpected fetch " + path);
  },
};
elements.set("app", new Element("app"));
elements.set("auth", new Element("auth"));
vm.createContext(context);
vm.runInContext(process.env.APP_JS, context);

(async () => {
  await new Promise(resolve => setTimeout(resolve, 0));
  const staleList = context.showSessions("d", "daemon");
  const freshList = context.showSessions("d", "daemon");
  sessionListResolvers[1](response({ sessions: [{ ID: "sid", Preview: "" }] }));
  await freshList;
  await context.showChat("d", "sid");
  if (!appHTML.includes('class="session-id"')) {
    throw new Error("expected chat detail before stale response, got: " + appHTML);
  }
  sessionListResolvers[0](response({ sessions: [{ ID: "sid", Preview: "late" }] }));
  await staleList;
  if (appHTML.includes("· sessions")) {
    throw new Error("stale sessions response replaced chat detail");
  }
})().catch(err => {
  console.error(err && err.stack || err);
  process.exit(1);
});
`
	cmd := exec.Command(node, "-e", script)
	cmd.Env = append(os.Environ(), "APP_JS="+string(appJS))
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
}

func TestWeb_CommanderAppRendersStatusAndErrorEvents(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable for commander app SSE test")
	}
	appJS, err := os.ReadFile("assets/app.js")
	require.NoError(t, err)

	script := `
const vm = require("node:vm");

const context = {
  console,
  setTimeout,
  TextDecoder,
  window: { open: () => {} },
  document: {
    getElementById: () => ({ innerHTML: "", style: {}, appendChild: () => {}, querySelector: () => ({}) }),
    createElement: () => ({ textContent: "", appendChild: () => {}, querySelector: () => ({}) }),
  },
  fetch: async path => {
    if (path === "/api/commander/daemons") return { ok: false, status: 401 };
    throw new Error("unexpected fetch " + path);
  },
};
vm.createContext(context);
vm.runInContext(process.env.APP_JS, context);

const assistant = { textContent: "" };
context.handleSSE('event: status\ndata: {"text":"codex running"}', assistant);
if (assistant.textContent !== "Codex 正在回答…") {
  throw new Error("status not rendered: " + assistant.textContent);
}
context.handleSSE('event: chunk\ndata: {"text":"OK"}', assistant);
if (assistant.textContent !== "Codex 正在回答…\nOK") {
  throw new Error("chunk should keep in-flight status: " + assistant.textContent);
}
context.handleSSE('event: done\ndata: {"result":{"summary":"OK"}}', assistant);
if (assistant.textContent !== "OK\n已回答完毕") {
  throw new Error("done status not rendered: " + assistant.textContent);
}
context.handleSSE('event: error\ndata: {"code":"backend_unavailable","message":"codex exit"}', assistant);
if (!assistant.textContent.includes("codex exit")) {
  throw new Error("error message not rendered: " + assistant.textContent);
}
`
	cmd := exec.Command(node, "-e", script)
	cmd.Env = append(os.Environ(), "APP_JS="+string(appJS))
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
}

func TestWeb_CommanderAppDisablesSendWhileTurnInFlight(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable for commander app turn state test")
	}
	appJS, err := os.ReadFile("assets/app.js")
	require.NoError(t, err)

	script := `
const vm = require("node:vm");

const elements = new Map();
class Element {
  constructor(id) {
    this.id = id;
    this.children = [];
    this.textContent = "";
    this.className = "";
    this.value = "";
    this.disabled = false;
    this.scrollTop = 0;
    this.scrollHeight = 0;
    this._innerHTML = "";
  }
  appendChild(child) { this.children.push(child); return child; }
  querySelector(selector) {
    if (!elements.has(selector)) elements.set(selector, new Element(selector));
    return elements.get(selector);
  }
  set innerHTML(value) { this._innerHTML = value; }
  get innerHTML() { return this._innerHTML; }
}
function getElement(id) {
  if (!elements.has(id)) elements.set(id, new Element(id));
  return elements.get(id);
}
const context = {
  console,
  setTimeout,
  TextDecoder,
  window: { open: () => {} },
  document: {
    getElementById: getElement,
    createElement: tag => new Element(tag),
  },
  fetch: async path => {
    if (path === "/api/commander/daemons") return { ok: false, status: 401 };
    if (path === "/api/commander/daemons/d/sessions/s/turn") {
      return { ok: true, body: { getReader: () => ({ read: async () => ({ done: true }) }) } };
    }
    throw new Error("unexpected fetch " + path);
  },
};
elements.set("app", new Element("app"));
elements.set("auth", new Element("auth"));
elements.set("chat", new Element("chat"));
elements.set("prompt", new Element("prompt"));
elements.set("send", new Element("send"));
vm.createContext(context);
vm.runInContext(process.env.APP_JS, context);

(async () => {
  const prompt = elements.get("prompt");
  const send = elements.get("send");
  prompt.value = "go";
  const turn = context.sendTurn("d", "s");
  if (!send.disabled) {
    throw new Error("send button was not disabled while turn was in flight");
  }
  await turn;
  if (send.disabled) {
    throw new Error("send button was not re-enabled after turn completed");
  }
})().catch(err => {
  console.error(err && err.stack || err);
  process.exit(1);
});
`
	cmd := exec.Command(node, "-e", script)
	cmd.Env = append(os.Environ(), "APP_JS="+string(appJS))
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
}
