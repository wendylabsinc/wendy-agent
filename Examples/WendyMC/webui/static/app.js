(() => {
  const term = new Terminal({
    fontFamily: 'ui-monospace, "SF Mono", Menlo, Consolas, monospace',
    fontSize: 13,
    convertEol: true,
    scrollback: 5000,
    theme: {
      background: "#1a1b1f",
      foreground: "#e6e6e6",
      cursor: "#4ade80",
    },
    disableStdin: true,
  });
  const fit = new FitAddon.FitAddon();
  term.loadAddon(fit);
  term.open(document.getElementById("terminal"));
  fit.fit();
  window.addEventListener("resize", () => fit.fit());

  // ---- console websocket ----
  let ws;
  let reconnectDelay = 1000;
  function connectWS() {
    const proto = location.protocol === "https:" ? "wss:" : "ws:";
    ws = new WebSocket(`${proto}//${location.host}/ws/console`);
    ws.onopen = () => {
      reconnectDelay = 1000;
      term.writeln("\x1b[32m[connected to console]\x1b[0m");
    };
    ws.onmessage = (e) => term.writeln(e.data);
    ws.onclose = () => {
      term.writeln("\x1b[33m[console disconnected, retrying…]\x1b[0m");
      setTimeout(connectWS, reconnectDelay);
      reconnectDelay = Math.min(reconnectDelay * 2, 10000);
    };
    ws.onerror = () => ws.close();
  }
  connectWS();

  // ---- command form ----
  const form = document.getElementById("cmd-form");
  const input = document.getElementById("cmd-input");
  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    const cmd = input.value.trim();
    if (!cmd) return;
    input.value = "";
    term.writeln(`\x1b[36m> ${cmd}\x1b[0m`);
    try {
      const r = await fetch("/api/command", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ cmd }),
      });
      const data = await r.json();
      if (!r.ok) {
        term.writeln(`\x1b[31m! ${data.detail || r.statusText}\x1b[0m`);
      } else if (data.response) {
        data.response.split("\n").forEach((line) => term.writeln(line));
      }
    } catch (err) {
      term.writeln(`\x1b[31m! ${err}\x1b[0m`);
    }
  });

  // ---- status polling ----
  const pill = document.getElementById("status-pill");
  const elVersion = document.getElementById("meta-version");
  const elMotd = document.getElementById("meta-motd");
  const elLatency = document.getElementById("meta-latency");
  const elCount = document.getElementById("player-count");
  const elList = document.getElementById("player-list");

  function setPill(state, text) {
    pill.dataset.state = state;
    pill.textContent = text;
  }

  async function poll() {
    try {
      const r = await fetch("/api/status");
      const s = await r.json();
      if (s.online) {
        setPill("online", "Online");
        elVersion.textContent = s.version;
        elMotd.textContent = s.motd;
        elLatency.textContent = `${s.latency_ms} ms`;
        elCount.textContent = `${s.players.online}/${s.players.max}`;
        elList.innerHTML = "";
        if (s.players.sample && s.players.sample.length) {
          for (const name of s.players.sample) {
            const li = document.createElement("li");
            li.textContent = name;
            elList.appendChild(li);
          }
        } else if (s.players.online > 0) {
          const li = document.createElement("li");
          li.className = "muted";
          li.textContent = `${s.players.online} online (names hidden)`;
          elList.appendChild(li);
        } else {
          elList.innerHTML = '<li class="muted">No players online</li>';
        }
      } else {
        setPill("starting", "Starting / offline");
        elLatency.textContent = "—";
      }
    } catch (e) {
      setPill("offline", "Web UI error");
    }
  }
  poll();
  setInterval(poll, 5000);

  // ---- restart ----
  document.getElementById("restart-btn").addEventListener("click", async () => {
    if (!confirm("Send `stop` to the server? It will auto-restart (~30–60s).")) return;
    setPill("starting", "Restarting…");
    try {
      const r = await fetch("/api/restart", { method: "POST" });
      const data = await r.json();
      if (!r.ok) {
        term.writeln(`\x1b[31m! restart failed: ${data.detail || r.statusText}\x1b[0m`);
      } else {
        term.writeln(`\x1b[33m[restart requested]\x1b[0m`);
      }
    } catch (e) {
      term.writeln(`\x1b[31m! ${e}\x1b[0m`);
    }
  });
})();
