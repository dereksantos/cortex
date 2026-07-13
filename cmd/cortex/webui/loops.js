// loops.js — M6.7f (owner amendment A4): the loops screen. Fetches GET
// /api/loops (serve_loops.go, live since M6.7b — returns loopsViewModel:
// every registered loop's spec plus run history) and renders it into the
// #loops container: per-loop enable/disable + run-now buttons wired to
// M6.7d/e's POST /api/loops/{name}/enable|disable|run-now, plus a create
// form wired to M6.7c's POST /api/loops. No new endpoint needed — the
// read+write surface was already complete before this screen (mirrors
// models.js's M5.3e posture: this file is the last screen wired onto an
// already-complete API). Kept as its own file per M5.3a's mechanical size
// caps — same reasoning M5.3c4/M5.3d/M5.3e used to split sse.js/
// landscape.js/models.js out. Reuses authToken()/apiFetch() from app.js as
// plain global functions (no module system, per D9) — index.html loads
// app.js before this file.

// postJSON issues an authenticated POST against path with a JSON body
// (or no body when omitted), returning the parsed JSON response. Shared by
// every write action below (create/enable/disable/run-now) so each one is
// a one-line call plus its own success/failure handling.
function postJSON(path, body) {
  const opts = {
    method: "POST",
    headers: { Authorization: "Bearer " + authToken() },
  };
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

// renderLoopRow appends one loop's name/project/prompt/interval/enabled
// state, its run history, and enable/disable + run-now controls to list.
function renderLoopRow(list, loop) {
  const row = document.createElement("li");

  const name = document.createElement("strong");
  name.textContent = loop.name + " (" + loop.project + "): ";
  row.appendChild(name);

  const prompt = document.createElement("span");
  prompt.textContent = loop.prompt;
  row.appendChild(prompt);

  const meta = document.createElement("div");
  const interval = loop.interval_minutes ? loop.interval_minutes + " min" : "manual only";
  meta.textContent = interval + " · " + (loop.enabled ? "enabled" : "disabled");
  row.appendChild(meta);

  const status = document.createElement("span");

  const toggleBtn = document.createElement("button");
  toggleBtn.type = "button";
  toggleBtn.textContent = loop.enabled ? "Disable" : "Enable";
  toggleBtn.addEventListener("click", () => {
    status.textContent = "Saving…";
    const action = loop.enabled ? "disable" : "enable";
    postJSON("/api/loops/" + encodeURIComponent(loop.name) + "/" + action)
      .then(() => loadLoops())
      .catch((err) => {
        status.textContent = "Failed: " + err.message;
      });
  });
  row.appendChild(toggleBtn);

  const runBtn = document.createElement("button");
  runBtn.type = "button";
  runBtn.textContent = "Run now";
  runBtn.addEventListener("click", () => {
    status.textContent = "Running…";
    postJSON("/api/loops/" + encodeURIComponent(loop.name) + "/run-now")
      .then(() => loadLoops())
      .catch((err) => {
        status.textContent = "Failed: " + err.message;
      });
  });
  row.appendChild(runBtn);
  row.appendChild(status);

  const runs = loop.runs || [];
  if (runs.length > 0) {
    const runList = document.createElement("ul");
    for (const r of runs) {
      const item = document.createElement("li");
      item.textContent = r.timestamp + ": " + r.outcome + (r.reason ? " (" + r.reason + ")" : "");
      runList.appendChild(item);
    }
    row.appendChild(runList);
  }

  list.appendChild(row);
}

// renderCreateForm appends a name/project/prompt/interval create form to
// container, POSTing to /api/loops and re-rendering on success.
function renderCreateForm(container) {
  const form = document.createElement("form");

  const nameInput = document.createElement("input");
  nameInput.type = "text";
  nameInput.placeholder = "name";
  form.appendChild(nameInput);

  const projectInput = document.createElement("input");
  projectInput.type = "text";
  projectInput.placeholder = "project";
  form.appendChild(projectInput);

  const promptInput = document.createElement("input");
  promptInput.type = "text";
  promptInput.placeholder = "prompt";
  form.appendChild(promptInput);

  const intervalInput = document.createElement("input");
  intervalInput.type = "number";
  intervalInput.placeholder = "interval minutes (0 = manual only)";
  form.appendChild(intervalInput);

  const button = document.createElement("button");
  button.type = "submit";
  button.textContent = "Create loop";
  form.appendChild(button);

  const status = document.createElement("span");
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
// innerHTML with response data, matching renderModels'/renderDashboard's
// posture.
function renderLoops(vm, container) {
  container.textContent = "";

  const loops = vm.loops || [];
  if (loops.length === 0) {
    const empty = document.createElement("p");
    empty.textContent = "No loops registered yet.";
    container.appendChild(empty);
  } else {
    const list = document.createElement("ul");
    for (const loop of loops) {
      renderLoopRow(list, loop);
    }
    container.appendChild(list);
  }

  const heading = document.createElement("h3");
  heading.textContent = "Create a loop";
  container.appendChild(heading);
  renderCreateForm(container);
}

// loadLoops no-ops when the #loops container is absent (dashboard/session/
// landscape/models-only pages), matching loadDashboard/loadSession/
// loadLandscape/loadModels' posture.
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
