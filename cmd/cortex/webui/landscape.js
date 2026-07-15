// landscape.js — the landscape screen. Fetches GET /api/landscape
// (serve_landscape.go) and renders the ScanReport JSON
// (buildLandscapeViewModel) into the #landscape container using the
// design-system .lgrid/.lbox/.lrow/table.projects/.privacy classes from
// app.css. Kept as its own file per the mechanical JS size caps. Reuses
// el()/authToken()/apiFetch() from app.js as plain global functions (no
// module system) — index.html loads app.js before this file.

// renderProbeBox appends one .lbox (Tools or Runtimes) listing each probe
// as a .lrow: mono name, a found/not-found chip, and its path.
function renderProbeBox(title, items) {
  const rows = (items || []).map((item) =>
    el("div", { className: "lrow" }, [
      el("span", { className: "mono", textContent: item.Name }),
      el("span", { className: "chip ok", textContent: "found" }),
      el("span", { className: "path", textContent: item.Path || "—" }),
    ]),
  );
  if (rows.length === 0) {
    rows.push(el("div", { className: "lrow" }, [el("span", { className: "dim", textContent: "None found." })]));
  }
  return el("div", { className: "lbox" }, [el("h4", { textContent: title }), ...rows]);
}

// renderProjectsBox appends the "projects with AI markers" table, one row
// per detected project: path, then a chip per marker.
function renderProjectsBox(projects) {
  const rows = (projects || []).map((p) => {
    const chips = (p.Markers || []).map((m) => el("span", { className: "chip mute", textContent: m }));
    return el("tr", {}, [
      el("td", { className: "mono", textContent: p.Path }),
      el("td", {}, chips.length ? chips : [el("span", { className: "dim", textContent: "—" })]),
    ]);
  });
  const table = el("table", { className: "projects" }, [
    el("tr", {}, [el("th", { textContent: "path" }), el("th", { textContent: "markers" })]),
    ...rows,
  ]);
  return el("div", { className: "lbox" }, [
    el("h4", { textContent: "Projects with AI markers" }),
    table,
    el("div", {
      className: "privacy",
      textContent: "read-only · names and paths only, never file contents · local-only",
    }),
  ]);
}

// renderNoRootsCard replaces the bare 412-text-in-the-corner rendering with
// a styled .lbox empty state: what happened, why, and the exact command to
// fix it, plus a pointer to the other way roots get set (the greeting
// conversation).
function renderNoRootsCard(container) {
  container.textContent = "";
  container.appendChild(
    el("div", { className: "lbox" }, [
      el("h4", { textContent: "No scan roots configured" }),
      el("p", {
        className: "dim",
        textContent: "Cortex hasn't been told where your code lives yet, so there's nothing to scan.",
      }),
      el("p", {}, [
        "Fix it: ",
        el("code", { className: "codechip", textContent: "cortex scan --register --root ~/eng" }),
      ]),
      el("p", {
        className: "dim",
        textContent: "The greeting conversation can also set scan roots for you, the first time you talk to cortex.",
      }),
    ]),
  );
}

// postJSON issues an authenticated POST with no body against an /api/...
// path. apiFetch (app.js) is GET-only; this screen is the only one that
// needs a POST (the Rescan button below), so the helper lives here rather
// than growing app.js's shared surface.
function postJSON(path) {
  return fetch(path, { method: "POST", headers: { Authorization: "Bearer " + authToken() } });
}

// fmtScannedAt renders a report's scanned_at (an RFC3339 string) as a local
// date+time; falls back to the raw string if it doesn't parse.
function fmtScannedAt(iso) {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString();
}

// renderLandscapeHead builds the header row above the report: mono
// "roots:" + one chip per root, a "from last scan" mute chip when the
// report came from the journal fallback (no roots persisted yet, GET
// /api/landscape read the most recent landscape.scan event instead of
// scanning live), a muted "scanned <time>" note, and the Rescan button.
// onRescan is wired by loadLandscape below so this stays a pure render.
function renderLandscapeHead(report, onRescan) {
  const roots = report.roots || [];
  const rootChips = roots.length
    ? roots.map((r) => el("span", { className: "chip mute", textContent: r }))
    : [el("span", { className: "dim", textContent: "(none)" })];

  const kids = [el("span", { className: "mono dim", textContent: "roots:" }), ...rootChips];
  if (report.source === "journal") {
    kids.push(el("span", { className: "chip mute", textContent: "from last scan" }));
  }
  if (report.scanned_at) {
    kids.push(el("span", { className: "dim status-text", textContent: "scanned " + fmtScannedAt(report.scanned_at) }));
  }

  const rescanBtn = el("button", { className: "btn primary", textContent: "Rescan" });
  rescanBtn.addEventListener("click", () => onRescan(rescanBtn));
  kids.push(rescanBtn);

  return el("div", { className: "land-head" }, kids);
}

// renderLandscape writes a landscape report (GET /api/landscape's or POST
// /api/landscape/rescan's JSON shape) into the #landscape container via
// plain DOM writes — textContent only, never innerHTML, matching app.js's
// untrusted-ish-local-string posture.
function renderLandscape(report, container, onRescan) {
  container.textContent = "";

  if (report.truncated) {
    container.appendChild(
      el("p", { className: "load-note", textContent: "Scan truncated — results may be incomplete." }),
    );
  }

  container.appendChild(renderLandscapeHead(report, onRescan));

  container.appendChild(
    el("div", { className: "lgrid" }, [
      renderProbeBox("Harnesses & editors", report.tools),
      renderProbeBox("Local runtimes", report.runtimes),
    ]),
  );
  container.appendChild(renderProjectsBox(report.projects));
}

// runRescan disables the Rescan button, POSTs /api/landscape/rescan, and
// re-renders the container with the fresh (always source:"live") report on
// success — or restores the button with an inline error on failure.
function runRescan(container, btn) {
  btn.disabled = true;
  btn.textContent = "Scanning…";
  postJSON("/api/landscape/rescan")
    .then((resp) => {
      if (!resp.ok) {
        throw new Error("POST /api/landscape/rescan: " + resp.status);
      }
      return resp.json();
    })
    .then((report) => renderLandscape(report, container, (b) => runRescan(container, b)))
    .catch((err) => {
      btn.disabled = false;
      btn.textContent = "Rescan";
      container.appendChild(el("p", { className: "load-note", textContent: "Rescan failed: " + err.message }));
    });
}

// loadLandscape no-ops when the #landscape container is absent/hidden. A
// 412 response means ErrNoScanRoots — no roots persisted AND nothing has
// ever been journaled — surfaced as a distinct empty-state card.
function loadLandscape() {
  const container = document.getElementById("landscape");
  if (!container) {
    return;
  }
  container.textContent = "Loading landscape…";
  apiFetch("/api/landscape")
    .then((resp) => {
      if (resp.status === 412) {
        renderNoRootsCard(container);
        return null;
      }
      if (!resp.ok) {
        throw new Error("GET /api/landscape: " + resp.status);
      }
      return resp.json();
    })
    .then((report) => {
      if (report) {
        renderLandscape(report, container, (btn) => runRescan(container, btn));
      }
    })
    .catch((err) => {
      container.textContent = "Failed to load landscape: " + err.message;
    });
}

loadLandscape();
