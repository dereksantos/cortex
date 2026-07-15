// dashboard.js — the projects screen's rendering. Split out of app.js to
// keep app.js under the per-file JS size cap (webui_jscap_test.go);
// app.js's loadDashboard() still owns the GET /api/dashboard fetch call
// (that endpoint string is asserted directly against app.js's source) and
// hands the parsed dashboardViewModel to renderDashboard, defined here.
// Reuses el() from app.js as a plain global function — no module system,
// index.html loads app.js before this file.

// changeChip describes a project's git change/clean state for its card.
function changeChip(p) {
  const cls = p.change_error ? "bad" : p.active_change ? "amb" : p.clean ? "ok" : "mute";
  const text = p.change_error
    ? "change status: " + p.change_error
    : p.active_change
      ? "change open · " + p.branch
      : p.clean
        ? "clean · " + p.branch
        : p.branch + " · dirty";
  return el("span", { className: "chip " + cls, textContent: text });
}

// fmtSessionDate renders a sessionSummary's mod_time (an RFC3339 string, per
// serve_routes.go's sessionSummary) as a short locale date; falls back to
// the raw string if it doesn't parse.
function fmtSessionDate(iso) {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleDateString();
}

// sessionRow builds one two-line session row for a project card: the
// session's first message (truncated) on line 1, mono id/count/date meta
// on line 2 — replaces the old flat "id · N msgs" line so a 50-session
// project reads as content, not a wall of near-identical rows.
function sessionRow(project, s) {
  const first = (s.first || "(no messages)").slice(0, 90);
  return el("li", {}, [
    el(
      "a",
      {
        className: "srow",
        href: "?project=" + encodeURIComponent(project) + "&session=" + encodeURIComponent(s.id),
      },
      [
        el("div", { className: "srow-first", textContent: first }),
        el("div", {
          className: "srow-meta mono",
          textContent: s.id + " · " + s.messages + " msgs · " + fmtSessionDate(s.mod_time),
        }),
      ],
    ),
  ]);
}

// emptySessionRow builds one compact row for the collapsed "N empty
// sessions" details body — id and date only, since there's no first
// message to show.
function emptySessionRow(project, s) {
  return el("li", {}, [
    el("a", {
      className: "srow-empty mono",
      href: "?project=" + encodeURIComponent(project) + "&session=" + encodeURIComponent(s.id),
      textContent: s.id + " · " + fmtSessionDate(s.mod_time),
    }),
  ]);
}

// renderSessionList appends a project card's session list: non-empty
// sessions render as sessionRow()s (at most 8 shown inline, the rest behind
// a "show all N sessions" <details>); empty (0-message) sessions collapse
// behind one "N empty sessions" <details> line instead of padding out the
// list with near-useless rows.
function renderSessionList(card, p) {
  const sessions = p.sessions || [];
  const nonEmpty = sessions.filter((s) => s.messages > 0);
  const empty = sessions.filter((s) => s.messages === 0);

  if (nonEmpty.length > 0) {
    const shown = nonEmpty.slice(0, 8);
    const rest = nonEmpty.slice(8);
    card.appendChild(el("ul", { className: "psessions" }, shown.map((s) => sessionRow(p.name, s))));
    if (rest.length > 0) {
      card.appendChild(
        el("details", { className: "more-sessions" }, [
          el("summary", { className: "mono dim", textContent: "show all " + nonEmpty.length + " sessions" }),
          el("ul", { className: "psessions" }, rest.map((s) => sessionRow(p.name, s))),
        ]),
      );
    }
  }
  if (empty.length > 0) {
    card.appendChild(
      el("details", { className: "empty-sessions" }, [
        el("summary", { className: "mono dim", textContent: empty.length + " empty sessions" }),
        el("ul", { className: "psessions" }, empty.map((s) => emptySessionRow(p.name, s))),
      ]),
    );
  }
}

// renderProjectCard builds one .pcard: name, root, change chip, session
// count + an "Open session" link to the first non-empty session (falling
// back to any session if every one is empty), and the redesigned session
// list.
function renderProjectCard(p) {
  const sessions = p.sessions || [];
  const nonEmpty = sessions.filter((s) => s.messages > 0);
  const top = el("div", { className: "pcard-top" }, [
    el("span", { className: "pname", textContent: p.name }),
    changeChip(p),
    el("span", { className: "path", textContent: p.root }),
  ]);

  const metaKids = [el("span", { textContent: sessions.length + " session" + (sessions.length === 1 ? "" : "s") })];
  const openTarget = nonEmpty[0] || sessions[0];
  if (openTarget) {
    metaKids.push(
      el("a", {
        className: "btn primary",
        href: "?project=" + encodeURIComponent(p.name) + "&session=" + encodeURIComponent(openTarget.id),
        textContent: "Open session",
      }),
    );
  }
  const card = el("div", { className: "pcard" }, [top, el("div", { className: "pmeta" }, metaKids)]);
  renderSessionList(card, p);
  return card;
}

// renderDashboard writes a dashboard view-model (dashboardViewModel's JSON
// shape) into the #dashboard container via plain DOM writes — textContent
// only, never innerHTML with response data, since project names/paths are
// untrusted-ish local-filesystem strings.
function renderDashboard(vm, container) {
  container.textContent = "";
  if (!vm.projects || vm.projects.length === 0) {
    container.appendChild(el("p", { className: "empty-note", textContent: "No registered projects yet." }));
    return;
  }
  container.appendChild(el("div", { className: "cards" }, vm.projects.map(renderProjectCard)));
}
