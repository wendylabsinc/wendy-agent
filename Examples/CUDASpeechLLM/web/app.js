const $ = (selector) => document.querySelector(selector);

const elements = {
  body: document.body,
  conversation: $("#conversation"), messages: $("#messages"), welcome: $("#welcome"),
  listen: $("#listenButton"), orbLabel: $(".orb-label"), phase: $("#phaseLabel"),
  detail: $("#phaseDetail"), sourcePill: $("#sourcePill"), source: $("#sourceText"),
  reset: $("#resetButton"), textForm: $("#textForm"), textInput: $("#textInput"),
};

let state = { phase: "loading", listening: false, capture_backend: "detecting" };
let assistantBubble = null;
let assistantText = "";

function post(path, payload = {}) {
  return fetch(path, {
    method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(payload),
  }).then(async (response) => {
    const body = await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(body.error || `Request failed (${response.status})`);
    return body;
  });
}

function phaseCopy(phase) {
  return {
    loading: ["Loading model", "The voice service will unlock automatically"],
    ready: ["Ready when you are", "Press the orb to use the Spark microphone"],
    calibrating: ["Calibrating the room", "A quiet second helps detect natural turns"],
    cooldown: ["Listening will resume automatically", "Waiting briefly for quieter room audio"],
    listening: ["Listening", "Speak naturally—silence ends your turn"],
    hearing: ["I hear you", "Keep talking"],
    thinking: ["Thinking", "Understanding your voice turn"],
    speaking: ["Speaking", "Kokoro · af_heart neural voice"],
    error: ["Audio needs attention", state.last_error || "Check the Spark's ALSA capture source"],
  }[phase] || ["Getting ready", ""];
}

function renderState(next) {
  state = { ...state, ...next };
  const [title, detail] = phaseCopy(state.phase);
  elements.phase.textContent = title;
  elements.detail.textContent = detail;
  elements.listen.disabled = state.phase === "loading";
  elements.listen.setAttribute("aria-pressed", String(Boolean(state.listening)));
  elements.listen.classList.toggle("active", Boolean(state.listening));
  elements.body.classList.toggle("listening", Boolean(state.listening));
  elements.orbLabel.textContent = state.auto_resume_pending
    ? "Resuming automatically"
    : state.listening ? "Stop listening" : "Start listening";
  const sourceReady = state.capture_backend && !["detecting", "unavailable"].includes(state.capture_backend);
  elements.source.textContent = sourceReady
    ? `${state.capture_backend} · ${state.capture_source || "default mic"}`
    : state.phase === "loading" ? "Preparing model" : "Spark microphone";
  elements.sourcePill.classList.toggle("ready", state.phase !== "loading" && state.phase !== "error");
  elements.sourcePill.classList.toggle("error", state.phase === "error");
}

function addMessage(role, text, meta = "") {
  elements.welcome.classList.add("hidden");
  const item = document.createElement("article");
  item.className = `message ${role}`;
  const label = document.createElement("span");
  label.className = "message-label";
  label.textContent = meta || (role === "user" ? "You" : "Ultravox");
  const bubble = document.createElement("div");
  bubble.className = "bubble";
  bubble.textContent = text;
  item.append(label, bubble);
  elements.messages.append(item);
  elements.conversation.scrollTo({ top: elements.conversation.scrollHeight, behavior: "smooth" });
  return bubble;
}

function handleEvent(message) {
  if (message.state) renderState(message.state);
  const { event, data = {} } = message;
  if (event === "level") {
    const visual = Math.min(1, Math.max(0, (data.level || 0) * 12));
    document.documentElement.style.setProperty("--level", visual.toFixed(3));
  } else if (event === "user_turn") {
    const text = data.kind === "audio" ? `Voice message · ${Number(data.duration).toFixed(1)}s` : data.text;
    addMessage("user", text, data.kind === "audio" ? "You · Spark mic" : "You");
  } else if (event === "assistant_start") {
    assistantText = "";
    assistantBubble = addMessage("assistant", "", "Ultravox · live");
    assistantBubble.classList.add("thinking");
  } else if (event === "assistant_delta") {
    if (!assistantBubble) assistantBubble = addMessage("assistant", "", "Ultravox · live");
    assistantText += data.text || "";
    assistantBubble.textContent = assistantText;
    assistantBubble.classList.add("thinking");
    elements.conversation.scrollTop = elements.conversation.scrollHeight;
  } else if (event === "assistant_done") {
    if (data.suppressed || !assistantText.trim()) assistantBubble?.closest(".message")?.remove();
    assistantBubble?.classList.remove("thinking");
    assistantBubble = null;
  } else if (event === "error") {
    if (assistantBubble) {
      assistantBubble.classList.remove("thinking");
      assistantBubble.textContent = `I hit a local error: ${data.message}`;
      assistantBubble = null;
    }
  } else if (event === "reset") {
    elements.messages.replaceChildren();
    elements.welcome.classList.remove("hidden");
    assistantBubble = null;
    assistantText = "";
  }
}

function connectEvents() {
  const events = new EventSource("/api/events");
  events.onmessage = (event) => {
    try { handleEvent(JSON.parse(event.data)); } catch (error) { console.error(error); }
  };
  events.onerror = () => {
    elements.source.textContent = "Reconnecting…";
    elements.sourcePill.classList.remove("ready");
  };
}

elements.listen.addEventListener("click", async () => {
  if (state.generating) await post("/api/interrupt").catch(() => {});
  try { renderState(await post("/api/listening", { enabled: !state.listening })); }
  catch (error) { elements.detail.textContent = error.message; }
});

elements.textForm.addEventListener("submit", async (event) => {
  event.preventDefault();
  const text = elements.textInput.value.trim();
  if (!text) return;
  elements.textInput.value = "";
  try { await post("/api/message", { text }); }
  catch (error) { elements.detail.textContent = error.message; }
});

elements.reset.addEventListener("click", async () => {
  await post("/api/reset").catch(() => {});
});

fetch("/api/status").then((response) => response.json()).then(renderState).catch(() => {});
connectEvents();
