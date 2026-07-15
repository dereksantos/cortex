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

// renderLandscape writes a ScanReport (GET /api/landscape's JSON shape)
// into the #landscape container via plain DOM writes — textContent only,
// never innerHTML, matching app.js's untrusted-ish-local-string posture.
function renderLandscape(report, container) {
  container.textContent = "";

  if (report.truncated) {
    container.appendChild(
      el("p", { className: "load-note", textContent: "Scan truncated — results may be incomplete." }),
    );
  }

  const roots = report.roots || [];
  const rootChips = roots.length
    ? roots.map((r) => el("span", { className: "chip mute", textContent: r }))
    : [el("span", { className: "dim", textContent: "(none)" })];
  container.appendChild(
    el("div", { className: "land-head" }, [el("span", { className: "mono dim", textContent: "roots:" }), ...rootChips]),
  );

  container.appendChild(
    el("div", { className: "lgrid" }, [
      renderProbeBox("Harnesses & editors", report.tools),
      renderProbeBox("Local runtimes", report.runtimes),
    ]),
  );
  container.appendChild(renderProjectsBox(report.projects));
}

// loadLandscape no-ops when the #landscape container is absent/hidden. A
// 412 response means ErrNoScanRoots — onboarding hasn't asked where the
// user's code lives yet — surfaced as a distinct message.
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
        renderLandscape(report, container);
      }
    })
    .catch((err) => {
      container.textContent = "Failed to load landscape: " + err.message;
    });
}

loadLandscape();
