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

  table.appendChild(
    el("tr", {}, [
      el("td", { className: "mono strong", textContent: loop.name }),
      el("td", { className: "mono dim", textContent: loop.project }),
      el("td", { className: "mono", textContent: interval }),
      el("td", { className: "mono dim", textContent: bounds }),
      el("td", {}, [outcomeChip((loop.runs || [])[0])]),
      el("td", {}, [toggle]),
      el("td", {}, [runBtn, status]),
    ]),
  );
}

// renderRunLog appends the run-history list beneath the loops table.
function renderRunLog(container, loops) {
  const rows = [];
  for (const loop of loops) {
    for (const r of loop.runs || []) {
      rows.push(
        el("div", { className: "lrow" }, [
          el("b", { textContent: loop.name }),
          " " + r.outcome + (r.reason ? " (" + r.reason + ")" : ""),
          el("span", { className: "when", textContent: r.timestamp }),
        ]),
      );
    }
  }
  if (rows.length === 0) {
    return;
  }
  container.appendChild(el("div", { className: "runlog" }, [el("h4", { textContent: "run history · loop.run" }), ...rows]));
}

// renderCreateForm appends a name/project/prompt/interval create form,
// POSTing to /api/loops and re-rendering on success. Built via a direct
// createElement("form") call (not the el() helper) — a plain <form> is
// still how the create action collects its fields.
function renderCreateForm(container) {
  const form = document.createElement("form");
  form.className = "loop-create";
  form.appendChild(el("h4", { textContent: "Create a loop" }));

  const nameInput = el("input", { type: "text", placeholder: "name" });
  const projectInput = el("input", { type: "text", placeholder: "project" });
  const promptInput = el("input", { type: "text", placeholder: "prompt" });
  const intervalInput = el("input", { type: "number", placeholder: "interval minutes (0 = manual)" });
  const status = el("span", { className: "status-text" });
  for (const field of [nameInput, projectInput, promptInput, intervalInput]) {
    form.appendChild(field);
  }
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
    container.appendChild(el("p", { className: "empty-note", textContent: "No loops registered yet." }));
  } else {
    const table = el("table", { className: "loops" }, [
      el("tr", {}, [
        "loop",
        "project",
        "trigger",
        "bounds",
        "last run",
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
