// session.js — the session screen's transcript rendering: the breadcrumb
// header and per-entry rendering, including collapsible tool-result output.
// Split out of app.js to keep app.js under the per-file JS size cap
// (webui_jscap_test.go); app.js's loadSession() still owns the GET
// /api/projects/{name}/sessions/{id} fetch call (that endpoint string is
// asserted directly against app.js's source) and the turn composer
// (renderTurnInput, likewise asserted against app.js), and hands the parsed
// transcriptViewModel to renderSession, defined here. Reuses el() from
// app.js as a plain global function — no module system, index.html loads
// app.js before this file.

// toolResultSummary renders a mono one-liner for a tool result's <summary>:
// a "tool ·" label, the first ~80 chars collapsed to a single line, and the
// total character count — so a long outline/grep dump is scannable without
// rendering as a full-width prose wall.
function toolResultSummary(text) {
  const oneLine = text.replace(/\s+/g, " ").trim();
  const head = oneLine.length > 80 ? oneLine.slice(0, 80) + "…" : oneLine;
  return "tool · " + (head || "(empty)") + " · " + text.length + " chars";
}

// renderToolResult appends one tool-role entry as a mono <details> box:
// open inline for short output (≤ ~280 chars), collapsed by default for
// long output. The expanded body is a scrollable mono <pre> capped at
// ~320px so even a multi-thousand-char dump stays bounded.
function renderToolResult(container, e) {
  const text = e.content || "";
  container.appendChild(
    el("details", { className: "toolresult", open: text.length <= 280 }, [
      el("summary", { className: "mono", textContent: toolResultSummary(text) }),
      el("pre", { className: "toolresult-body mono", textContent: text }),
    ]),
  );
}

// renderEntry appends one transcript entry: a tool-result entry renders via
// renderToolResult (collapsible, mono); user/assistant text stays a .msg
// bubble as before. Any tool calls the entry carries still render as a
// .toolfeed block underneath, unchanged.
function renderEntry(container, e) {
  if (e.role === "tool") {
    renderToolResult(container, e);
  } else {
    const cls = e.role === "assistant" ? "agent" : "user";
    container.appendChild(
      el("div", { className: "msg " + cls }, [
        el("div", { className: "who", textContent: e.role }),
        el("div", { className: "bubble", textContent: e.content }),
      ]),
    );
  }
  if (e.tool_calls && e.tool_calls.length > 0) {
    const lines = e.tool_calls.map((tc) =>
      el("div", {}, [el("span", { className: "t", textContent: tc.name }), " " + tc.args]),
    );
    container.appendChild(el("div", { className: "toolfeed" }, lines));
  }
}

// renderSession writes a transcript view-model (transcriptViewModel's JSON
// shape) into the #session container via plain DOM writes — textContent
// only, never innerHTML with response data (transcript content is
// untrusted-ish local turn history). The header is a breadcrumb: a link
// back to the projects screen plus a mono "cortex / session <id>" trail.
function renderSession(vm, container) {
  container.textContent = "";
  container.appendChild(
    el("div", { className: "sess-head" }, [
      el("a", { className: "btn", href: "/?screen=projects", textContent: "← projects" }),
      el("span", { className: "crumb" }, ["cortex / session ", el("b", { textContent: vm.session_id })]),
    ]),
  );
  if (!vm.entries || vm.entries.length === 0) {
    container.appendChild(el("p", { className: "empty-note", textContent: "No turns yet." }));
    return;
  }
  for (const e of vm.entries) {
    renderEntry(container, e);
  }
}
