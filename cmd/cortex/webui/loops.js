// loops.js — the loops screen. Fetches GET /api/loops (serve_loops.go —
// returns loopsViewModel: every registered loop's spec plus run history)
// and renders it into the #loops container using the design-system
// table.loops/.toggle/.runlog classes from app.css: per-loop enable/
// disable + run-now controls wired to POST /api/loops/{name}/enable|
// disable|run-now, plus a create form wired to POST /api/loops. Reuses
// el()/authToken()/apiFetch() from app.js as plain global functions — no
// module system, index.html loads app.js before this file.

// postJSON issues an authenticated POST against path with a JSON body (or
// no body when omitted), returning the parsed JSON response.
function postJSON(path, body) {
  const opts = { method: "POST", headers: { Authorization: "Bearer " + authToken() } };
  if (body !== undefined) {
    opts.headers["Content-Type"] = "application/json";
    opts.body = JSON.stringify(body);
  }
  return fetch(path, opts).then((resp) => {
    if (!resp.ok) {
      throw new Error("POST " + path + ": " + resp.status);
    }
    return resp.json();
  });
}

// outcomeChip maps a run's outcome to a chip class (ok/amb/bad/mute).
function outcomeChip(run) {
  if (!run) {
    return el("span", { className: "chip mute", textContent: "no runs yet" });
  }
  const cls = run.outcome === "ok" ? "ok" : run.outcome === "skipped" ? "amb" : "bad";
  const text = run.outcome + (run.reason ? " · " + run.reason : "");
  return el("span", { className: "chip " + cls, textContent: text });
}

// relativeish renders an RFC3339 instant as a short "in Nm/Nh/Nd" — good
// enough for a glance, not a full calendar widget.
function relativeish(iso) {
  const mins = Math.round((new Date(iso).getTime() - Date.now()) / 60000);
  if (mins <= 0) return "due now";
  if (mins < 60) return "in " + mins + "m";
  const hours = Math.round(mins / 60);
  return hours < 24 ? "in " + hours + "h" : "in " + Math.round(hours / 24) + "d";
}

// nextRunCell renders a loop's derived next_run/next_reason (D11
// self-pacing) — an em dash when the loop will never auto-fire again
// (manual-only, DONE, or no run history yet).
function nextRunCell(loop) {
  if (!loop.next_run) {
    return el("span", { className: "mono dim", textContent: "—" });
  }
  const text = relativeish(loop.next_run) + (loop.next_reason ? " · " + loop.next_reason : "");
  return el("span", { className: "mono dim", textContent: text });
}

// disabledChip distinguishes WHY a disabled loop stopped firing (D11's
// terminal-state tuning): "done" (it finished on its own), "3 strikes"
// (auto-disabled after repeated failures), or plain "disabled" (a human
// flipped the toggle) — null for an enabled loop (no chip to show).
function disabledChip(loop) {
  if (loop.enabled) return null;
  const reason = loop.disabled_reason || "";
  if (reason.indexOf("done:") === 0) {
    return el("span", { className: "chip ok", textContent: "done" });
  }
  if (reason === "3 consecutive failures") {
    return el("span", { className: "chip bad", textContent: "3 strikes" });
  }
  return el("span", { className: "chip mute", textContent: "disabled" });
}

// renderLoopRow appends one <tr> for a loop: name/project/trigger/bounds,
// last-run outcome chip, an enable/disable toggle, and a run-now button.
function renderLoopRow(table, loop) {
  const status = el("span", { className: "status-text" });
  const interval = loop.interval_minutes ? "every " + loop.interval_minutes + "m" : "manual";
  const bounds = (loop.max_turns || 25) + " turns";

  const toggle = el("button", {
    type: "button",
    className: "toggle" + (loop.enabled ? "" : " off"),
    title: loop.enabled ? "Disable" : "Enable",
  });
  toggle.addEventListener("click", () => {
    status.textContent = "Saving…";
    const path = loop.enabled
      ? "/api/loops/" + encodeURIComponent(loop.name) + "/disable"
      : "/api/loops/" + encodeURIComponent(loop.name) + "/enable";
    postJSON(path)
      .then(() => loadLoops())
      .catch((err) => {
        status.textContent = "Failed: " + err.message;
      });
  });

  const runBtn = el("button", { type: "button", className: "btn", textContent: "Run now" });
  runBtn.addEventListener("click", () => {
    status.textContent = "Running…";
    postJSON("/api/loops/" + encodeURIComponent(loop.name) + "/run-now")
      .then(() => loadLoops())
      .catch((err) => {
        status.textContent = "Failed: " + err.message;
      });
  });

  const chip = disabledChip(loop);
  table.appendChild(
    el("tr", {}, [
      el("td", { className: "mono strong", textContent: loop.name }),
      el("td", { className: "mono dim", textContent: loop.project }),
      el("td", { className: "mono", textContent: interval }),
      el("td", { className: "mono dim", textContent: bounds }),
      el("td", {}, [outcomeChip((loop.runs || [])[0])]),
      el("td", {}, [nextRunCell(loop)]),
      el("td", {}, chip ? [toggle, chip] : [toggle]),
      el("td", {}, [runBtn, status]),
    ]),
  );
}

// renderRunLog appends the run-history list beneath the loops table.
function renderRunLog(container, loops) {
  const rows = [];
  for (const loop of loops) {
    for (const r of loop.runs || []) {
      const text = " " + r.outcome + (r.reason ? " (" + r.reason + ")" : "") + (r.next_reason ? " → " + r.next_reason : "");
      rows.push(
        el("div", { className: "lrow" }, [el("b", { textContent: loop.name }), text, el("span", { className: "when", textContent: r.timestamp })]),
      );
    }
  }
  if (rows.length === 0) {
    return;
  }
  container.appendChild(el("div", { className: "runlog" }, [el("h4", { textContent: "run history · loop.run" }), ...rows]));
}

// labeledField wraps an input with a small uppercase mono label above it
// (and an optional hint line below), so the create form's fields are
// identifiable without relying on a placeholder alone (a numeric interval
// field's placeholder gets clipped long before "0 = manual" is legible).
function labeledField(labelText, input, hint) {
  const kids = [el("label", { className: "field-label mono", textContent: labelText }), input];
  if (hint) {
    kids.push(el("span", { className: "field-hint mono dim", textContent: hint }));
  }
  return el("div", { className: "field" }, kids);
}

// renderCreateForm appends a name/project/prompt/interval create form,
// POSTing to /api/loops and re-rendering on success. Built via a direct
// createElement("form") call (not the el() helper) — a plain <form> is
// still how the create action collects its fields.
function renderCreateForm(container) {
  const form = document.createElement("form");
  form.className = "loop-create";
  form.appendChild(el("h4", { textContent: "Create a loop" }));

  const nameInput = el("input", { type: "text", placeholder: "nightly-links" });
  const projectInput = el("input", { type: "text", placeholder: "blog" });
  const promptInput = el("input", { type: "text", placeholder: "check for broken links and fix them" });
  const intervalInput = el("input", { type: "number", placeholder: "0" });
  const status = el("span", { className: "status-text" });
  form.appendChild(labeledField("NAME", nameInput));
  form.appendChild(labeledField("PROJECT", projectInput));
  form.appendChild(labeledField("PROMPT", promptInput));
  form.appendChild(labeledField("INTERVAL", intervalInput, "minutes · 0 = manual · min 5"));
  form.appendChild(el("button", { type: "submit", className: "btn primary", textContent: "Create loop" }));
  form.appendChild(status);

  form.addEventListener("submit", (ev) => {
    ev.preventDefault();
    status.textContent = "Creating…";
    postJSON("/api/loops", {
      name: nameInput.value,
      project: projectInput.value,
      prompt: promptInput.value,
      interval_minutes: parseInt(intervalInput.value, 10) || 0,
    })
      .then(() => loadLoops())
      .catch((err) => {
        status.textContent = "Failed: " + err.message;
      });
  });

  container.appendChild(form);
}

// renderLoops writes a loopsViewModel (GET /api/loops' JSON shape) into
// the #loops container via plain DOM writes — textContent only, never
// innerHTML with response data.
function renderLoops(vm, container) {
  container.textContent = "";

  const loops = vm.loops || [];
  if (loops.length === 0) {
    container.appendChild(
      el("div", { className: "lbox" }, [el("p", { className: "dim", textContent: "No loops registered yet." })]),
    );
  } else {
    const table = el("table", { className: "loops" }, [
      el("tr", {}, [
        "loop",
        "project",
        "trigger",
        "bounds",
        "last run",
        "next run",
        "",
        "",
      ].map((h) => el("th", { textContent: h }))),
    ]);
    for (const loop of loops) {
      renderLoopRow(table, loop);
    }
    container.appendChild(table);
    renderRunLog(container, loops);
  }

  renderCreateForm(container);
}

// loadLoops no-ops when the #loops container is absent/hidden.
function loadLoops() {
  const container = document.getElementById("loops");
  if (!container) {
    return;
  }
  container.textContent = "Loading loops…";
  apiFetch("/api/loops")
    .then((resp) => {
      if (!resp.ok) {
        throw new Error("GET /api/loops: " + resp.status);
      }
      return resp.json();
    })
    .then((vm) => renderLoops(vm, container))
    .catch((err) => {
      container.textContent = "Failed to load loops: " + err.message;
    });
}

loadLoops();
