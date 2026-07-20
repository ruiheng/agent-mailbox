package webui

const indexHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>waypost roundtable</title>
  <style>
    :root {
      color-scheme: light;
      --bg: #f6f4ef;
      --ink: #24211d;
      --muted: #756f66;
      --line: #d8d2c7;
      --panel: #fffdf8;
      --accent: #0f766e;
      --accent-soft: #d9f3ee;
      --warn: #9f3a2f;
      font-family: Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      background: var(--bg);
      color: var(--ink);
      min-height: 100vh;
    }
    button, select {
      font: inherit;
    }
    .shell {
      display: grid;
      grid-template-columns: 280px minmax(0, 1fr);
      min-height: 100vh;
    }
    aside {
      border-right: 1px solid var(--line);
      padding: 22px 18px;
      background: #ebe6dc;
    }
    .brand {
      font-size: 15px;
      font-weight: 700;
      margin-bottom: 22px;
    }
    .group-list {
      display: grid;
      gap: 6px;
    }
    .group-button {
      width: 100%;
      border: 0;
      background: transparent;
      color: var(--ink);
      text-align: left;
      padding: 9px 10px;
      border-radius: 6px;
      cursor: pointer;
      overflow-wrap: anywhere;
    }
    .group-button:hover, .group-button.active {
      background: var(--panel);
    }
    main {
      display: grid;
      grid-template-rows: auto minmax(0, 1fr);
      min-width: 0;
    }
    header {
      border-bottom: 1px solid var(--line);
      padding: 18px 28px;
      background: rgba(255, 253, 248, 0.82);
      backdrop-filter: blur(16px);
      position: sticky;
      top: 0;
      z-index: 2;
    }
    .title-row {
      display: flex;
      gap: 14px;
      align-items: center;
      justify-content: space-between;
    }
    h1 {
      margin: 0;
      font-size: 22px;
      line-height: 1.2;
      overflow-wrap: anywhere;
    }
    .status {
      color: var(--muted);
      font-size: 13px;
      white-space: nowrap;
    }
    .status.live::before {
      content: "";
      display: inline-block;
      width: 7px;
      height: 7px;
      margin-right: 7px;
      border-radius: 50%;
      background: var(--accent);
      box-shadow: 0 0 0 5px var(--accent-soft);
    }
    .meta {
      color: var(--muted);
      margin-top: 6px;
      font-size: 13px;
    }
    .timeline {
      overflow-y: auto;
      padding: 28px;
    }
    .empty, .error {
      color: var(--muted);
      padding: 40px 0;
      max-width: 560px;
    }
    .error { color: var(--warn); }
    .message {
      max-width: 920px;
      padding: 14px 0;
      animation: rise 160ms ease-out;
    }
    .message-header {
      display: grid;
      grid-template-columns: minmax(150px, 0.9fr) minmax(0, 1.6fr) minmax(148px, auto) auto;
      gap: 14px;
      align-items: start;
      padding: 12px 14px;
      background: #e9e2d7;
      border: 1px solid var(--line);
      border-bottom: 0;
      border-radius: 6px 6px 0 0;
    }
    .message-body {
      position: relative;
      padding: 16px 14px 18px;
      background: var(--panel);
      border: 1px solid var(--line);
      border-radius: 0 0 6px 6px;
    }
    .copy-button {
      position: absolute;
      top: 10px;
      right: 10px;
      border: 1px solid var(--line);
      border-radius: 5px;
      background: #f6f4ef;
      color: var(--muted);
      cursor: pointer;
      font-size: 12px;
      line-height: 1;
      padding: 6px 8px;
    }
    .copy-button:hover {
      color: var(--ink);
      border-color: #bfb6a8;
    }
    .header-field {
      min-width: 0;
    }
    .header-label {
      color: var(--muted);
      font-size: 11px;
      font-weight: 700;
      letter-spacing: 0;
      text-transform: uppercase;
      margin-bottom: 4px;
    }
    .header-value {
      color: var(--ink);
      font-size: 13px;
      font-weight: 650;
      overflow-wrap: anywhere;
    }
    .header-detail {
      color: var(--muted);
      font-size: 12px;
      margin-top: 5px;
      overflow-wrap: anywhere;
    }
    .body {
      white-space: pre-wrap;
      overflow-wrap: anywhere;
      line-height: 1.52;
      font-size: 15px;
      padding-right: 62px;
    }
    .read {
      white-space: nowrap;
    }
    @keyframes rise {
      from { opacity: 0; transform: translateY(6px); }
      to { opacity: 1; transform: translateY(0); }
    }
    @media (max-width: 760px) {
      .shell { grid-template-columns: 1fr; }
      aside {
        border-right: 0;
        border-bottom: 1px solid var(--line);
        padding: 14px;
      }
      .group-list {
        display: flex;
        overflow-x: auto;
      }
      .group-button {
        white-space: nowrap;
        width: auto;
      }
      header, .timeline { padding-left: 16px; padding-right: 16px; }
      .message-header {
        grid-template-columns: 1fr;
        gap: 8px;
      }
      .read { white-space: normal; }
      .title-row {
        align-items: flex-start;
        flex-direction: column;
      }
    }
  </style>
</head>
<body>
  <div class="shell">
    <aside>
      <div class="brand">waypost</div>
      <div id="groups" class="group-list"></div>
    </aside>
    <main>
      <header>
        <div class="title-row">
          <h1 id="title">Roundtable transcript</h1>
          <div id="status" class="status">Loading</div>
        </div>
        <div id="meta" class="meta"></div>
      </header>
      <section id="timeline" class="timeline"></section>
    </main>
  </div>
  <script>
    const defaultGroup = {{DEFAULT_GROUP}};
    const groupsEl = document.querySelector("#groups");
    const titleEl = document.querySelector("#title");
    const metaEl = document.querySelector("#meta");
    const statusEl = document.querySelector("#status");
    const timelineEl = document.querySelector("#timeline");
    let selectedGroup = defaultGroup || "";
    let source = null;
    const rendered = new Set();

    function setStatus(text, live) {
      statusEl.textContent = text;
      statusEl.classList.toggle("live", Boolean(live));
    }

    function groupURL(group, suffix) {
      return "/api/groups/" + encodeURIComponent(group) + "/" + suffix;
    }

    async function loadGroups() {
      const response = await fetch("/api/groups");
      if (!response.ok) throw new Error(await response.text());
      const payload = await response.json();
      const groups = payload.groups || [];
      if (!selectedGroup && groups.length) selectedGroup = groups[0].address;
      renderGroups(groups);
      if (selectedGroup) await loadTranscript(selectedGroup);
      else {
        setStatus("No groups", false);
        timelineEl.innerHTML = '<div class="empty">No group wayposts exist yet.</div>';
      }
    }

    function renderGroups(groups) {
      groupsEl.innerHTML = "";
      for (const group of groups) {
        const button = document.createElement("button");
        button.className = "group-button" + (group.address === selectedGroup ? " active" : "");
        button.textContent = group.address;
        button.addEventListener("click", () => {
          selectedGroup = group.address;
          renderGroups(groups);
          loadTranscript(selectedGroup);
        });
        groupsEl.appendChild(button);
      }
    }

    async function loadTranscript(group) {
      closeSource();
      rendered.clear();
      titleEl.textContent = group;
      metaEl.textContent = "";
      timelineEl.innerHTML = "";
      setStatus("Loading", false);
      const response = await fetch(groupURL(group, "transcript"));
      if (!response.ok) {
        setStatus("Error", false);
        timelineEl.innerHTML = '<div class="error">Unable to load transcript.</div>';
        return;
      }
      const payload = await response.json();
      const messages = payload.messages || [];
      for (const message of messages) appendMessage(message);
      if (!messages.length) timelineEl.innerHTML = '<div class="empty">No messages yet.</div>';
      metaEl.textContent = messages.length + " messages";
      startEvents(group, messages.length ? messages[messages.length - 1].message_id : "");
    }

    function startEvents(group, after) {
      closeSource();
      const url = groupURL(group, "events") + (after ? "?after=" + encodeURIComponent(after) : "");
      source = new EventSource(url);
      source.addEventListener("open", () => setStatus("Live", true));
      source.addEventListener("message", (event) => {
        const message = JSON.parse(event.data);
        if (timelineEl.querySelector(".empty")) timelineEl.innerHTML = "";
        appendMessage(message);
        metaEl.textContent = rendered.size + " messages";
      });
      source.addEventListener("error", () => setStatus("Reconnecting", false));
    }

    function closeSource() {
      if (source) source.close();
      source = null;
    }

    function appendMessage(message) {
      if (rendered.has(message.message_id)) return;
      rendered.add(message.message_id);
      const row = document.createElement("article");
      row.className = "message";
      const when = new Date(message.message_created_at);
      row.innerHTML = [
        '<div class="message-header">',
        '<div class="header-field">',
          '<div class="header-label">From</div>',
          '<div class="header-value from"></div>',
          '<div class="header-detail via"></div>',
        '</div>',
        '<div class="header-field">',
          '<div class="header-label">Subject</div>',
          '<div class="header-value subject"></div>',
        '</div>',
        '<div class="header-field">',
          '<div class="header-label">Time</div>',
          '<div class="header-value time"></div>',
        '</div>',
        '<div class="header-field read">',
          '<div class="header-label">Read</div>',
          '<div class="header-value read-value"></div>',
        '</div>',
        '</div>',
        '<div class="message-body">',
          '<button class="copy-button" type="button">Copy</button>',
          '<div class="body"></div>',
        '</div>',
      ].join("");
      row.querySelector(".from").textContent = message.display_sender || "unknown";
      const via = senderVia(message);
      row.querySelector(".via").textContent = via ? "Via: " + via : "";
      row.querySelector(".time").textContent = formatTimestamp(when, message.message_created_at);
      row.querySelector(".subject").textContent = message.subject || "(no subject)";
      row.querySelector(".body").textContent = message.body || "";
      row.querySelector(".read-value").textContent = message.read_count + "/" + message.eligible_count;
      row.querySelector(".copy-button").addEventListener("click", (event) => {
        copyBody(message.body || "", event.currentTarget);
      });
      timelineEl.appendChild(row);
      timelineEl.scrollTop = timelineEl.scrollHeight;
    }

    function senderVia(message) {
      if (!message.forwarded_from_address || !message.sender_address) return "";
      if (message.forwarded_from_address === message.sender_address) return "";
      return message.sender_address;
    }

    function formatTimestamp(date, fallback) {
      if (isNaN(date)) return fallback || "";
      const pad = (value) => String(value).padStart(2, "0");
      return [
        date.getFullYear(),
        "-",
        pad(date.getMonth() + 1),
        "-",
        pad(date.getDate()),
        " ",
        pad(date.getHours()),
        ":",
        pad(date.getMinutes()),
        ":",
        pad(date.getSeconds())
      ].join("");
    }

    async function copyBody(text, button) {
      try {
        if (navigator.clipboard && window.isSecureContext) {
          await navigator.clipboard.writeText(text);
        } else {
          fallbackCopy(text);
        }
        flashCopyButton(button, "Copied");
      } catch (error) {
        flashCopyButton(button, "Failed");
      }
    }

    function fallbackCopy(text) {
      const textarea = document.createElement("textarea");
      textarea.value = text;
      textarea.setAttribute("readonly", "");
      textarea.style.position = "fixed";
      textarea.style.top = "-1000px";
      document.body.appendChild(textarea);
      textarea.select();
      document.execCommand("copy");
      textarea.remove();
    }

    function flashCopyButton(button, label) {
      const previous = button.textContent;
      button.textContent = label;
      window.setTimeout(() => {
        button.textContent = previous;
      }, 1100);
    }

    loadGroups().catch((error) => {
      setStatus("Error", false);
      timelineEl.innerHTML = '<div class="error">' + error.message + '</div>';
    });
  </script>
</body>
</html>`
