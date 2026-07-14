// landscape.js — M5.3d: the landscape screen. Fetches GET /api/landscape
// (serve_landscape.go, live since M4.2c1) and renders the ScanReport JSON
// buildLandscapeViewModel (M5.2c) produces into the #landscape container.
// Kept as its own file rather than folded into app.js per M5.3a's
// mechanical JS size caps — app.js is already near the 300-line per-file
// cap after M5.3c's session-screen growth, the same reasoning M5.3c4 used
// to split sse.js out. Reuses authToken()/apiFetch() from app.js as plain
// global functions (no module system, per D9) — index.html loads app.js
// before this file.

// renderLandscapeSection appends a titled list of items (tools, runtimes,
// or projects) to the #landscape container, or a "none found" note when
// empty — matching renderDashboard/renderSession's empty-state convention
// in app.js.
function renderLandscapeSection(container, title, items, describe) {
  const heading = document.createElement("h3");
  heading.textContent = title;
  container.appendChild(heading);

  if (!items || items.length === 0) {
    const empty = document.createElement("p");
    empty.textContent = "None found.";
    container.appendChild(empty);
    return;
  }

  const list = document.createElement("ul");
  for (const item of items) {
    const row = document.createElement("li");
    row.textContent = describe(item);
    list.appendChild(row);
  }
  container.appendChild(list);
}

// renderLandscape writes a ScanReport (GET /api/landscape's JSON shape)
// into the #landscape container via plain DOM writes — textContent only,
// never innerHTML, matching app.js's untrusted-ish-local-string posture.
function renderLandscape(report, container) {
  container.textContent = "";

  if (report.truncated) {
    const warn = document.createElement("p");
    warn.textContent = "Scan truncated — results may be incomplete.";
    container.appendChild(warn);
  }

  const roots = document.createElement("p");
  const rootList = report.roots && report.roots.length ? report.roots.join(", ") : "(none)";
  roots.textContent = "Roots: " + rootList;
  container.appendChild(roots);

  renderLandscapeSection(container, "Tools", report.tools, (t) => t.Name + " (" + t.Path + ")");
  renderLandscapeSection(container, "Runtimes", report.runtimes, (r) => r.Name + " (" + r.Path + ")");
  renderLandscapeSection(
    container,
    "Projects",
    report.projects,
    (p) => p.Path + " [" + (p.Markers || []).join(", ") + "]",
  );
}

// loadLandscape no-ops when the #landscape container is absent
// (dashboard/session-only pages), matching loadDashboard/loadSession's
// posture in app.js. A 412 response means ErrNoScanRoots — onboarding
// (M1.7's greeting) hasn't asked where the user's code lives yet — surfaced
// as a distinct message rather than a generic fetch failure.
function loadLandscape() {
  const container = document.getElementById("landscape");
  if (!container) {
    return;
  }
  container.textContent = "Loading landscape…";
  apiFetch("/api/landscape")
    .then((resp) => {
      if (resp.status === 412) {
        throw new Error("no scan roots configured yet");
      }
      if (!resp.ok) {
        throw new Error("GET /api/landscape: " + resp.status);
      }
      return resp.json();
    })
    .then((report) => renderLandscape(report, container))
    .catch((err) => {
      container.textContent = "Failed to load landscape: " + err.message;
    });
}

loadLandscape();
