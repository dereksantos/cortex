// memory.js — the memory screen (docs/cross-source-learning.md piece 4).
// Fetches GET /api/projects for the picker, GET /api/memory?project=<name>
// for the tier-tagged note list, GET /api/memory/note?tier=&name=&project=
// for a note's full body — rendered with [[wikilinks]] highlighted, only
// clickable when the target exists in the currently loaded note set,
// navigating within this screen (no fetch-on-hover) — DELETE
// /api/memory/note?... for the human correction loop (two-click confirm,
// no modal), and GET /api/loops (already used by the loops screen)
// filtered to kind:"learn" firings for the recent-activity strip. Reuses
// el()/apiFetch()/queryParam() from app.js as plain global functions — no
// module system, index.html loads app.js before this file.

const memState = { project: queryParam("project"), notes: [] };

// fmtWhen renders an RFC3339 instant (or "") as a short locale date.
function fmtWhen(iso) {
  if (!iso) return "";
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleDateString();
}

// findNote looks up a wikilink target by name, preferring the current
// note's own tier (docs/cross-source-learning.md piece 3's "same-tier-by-
// default" recommendation), falling back to the other tier.
function findNote(name, preferTier) {
  const hits = memState.notes.filter((n) => n.name === name);
  return hits.find((n) => n.tier === preferTier) || hits[0] || null;
}

// renderBody splits a note body on [[wikilink]] tokens into plain text
// nodes plus .wikilink spans — clickable (and normally styled) only when
// the target exists in the loaded note set, else a plain .wikilink.missing
// span: visible as a link-shaped token, but inert (per the task: "clickable
// only if the target exists in the loaded set").
function renderBody(body, tier) {
  const frag = document.createDocumentFragment();
  const re = /\[\[([^\]]+)\]\]/g;
  let last = 0;
  let m;
  while ((m = re.exec(body))) {
    if (m.index > last) {
      frag.appendChild(document.createTextNode(body.slice(last, m.index)));
    }
    const target = findNote(m[1].trim(), tier);
    const span = el("span", {
      className: "wikilink" + (target ? "" : " missing"),
      textContent: "[[" + m[1] + "]]",
      title: target ? "open " + m[1] : "no such note in the loaded set",
    });
    if (target) {
      span.addEventListener("click", () => openNote(target.tier, target.name));
    }
    frag.appendChild(span);
    last = re.lastIndex;
  }
  frag.appendChild(document.createTextNode(body.slice(last)));
  return frag;
}

// openNote fetches one note's full body (GET /api/memory/note) and renders
// it into #mem-note-view, wiring a two-click-confirm delete button and
// wikilink navigation within the screen.
function openNote(tier, name) {
  const box = document.getElementById("mem-note-view");
  if (!box) return;
  box.textContent = "Loading…";
  const qs = "?tier=" + encodeURIComponent(tier) + "&name=" + encodeURIComponent(name) + "&project=" + encodeURIComponent(memState.project);
  apiFetch("/api/memory/note" + qs)
    .then((resp) => {
      if (!resp.ok) throw new Error("GET note: " + resp.status);
      return resp.json();
    })
    .then((note) => {
      box.textContent = "";
      const delBtn = el("button", { type: "button", className: "btn danger", textContent: "Delete" });
      let confirming = false;
      delBtn.addEventListener("click", () => {
        if (!confirming) {
          confirming = true;
          delBtn.textContent = "Confirm delete?";
          return;
        }
        delBtn.disabled = true;
        fetch("/api/memory/note" + qs, { method: "DELETE" })
          .then((resp) => {
            if (!resp.ok) throw new Error("DELETE note: " + resp.status);
            box.textContent = "";
            loadMemory();
          })
          .catch((err) => {
            delBtn.disabled = false;
            delBtn.textContent = "Failed: " + err.message;
          });
      });
      box.appendChild(
        el("div", { className: "mem-note" }, [
          el("div", { className: "mem-note-head" }, [
            el("span", { className: "chip " + (tier === "user" ? "info" : "amb"), textContent: tier }),
            el("span", { className: "mono strong", textContent: note.name }),
            el("span", { className: "mono dim", textContent: fmtWhen(note.updated) }),
            delBtn,
          ]),
          el("div", { className: "mem-note-body" }, [renderBody(note.body, tier)]),
        ]),
      );
    })
    .catch((err) => {
      box.textContent = "Failed to load note: " + err.message;
    });
}

// renderNoteRow appends one clickable .lrow for a note: tier chip, name,
// hook, and last-updated date — clicking loads it into the note view.
function renderNoteRow(list, n) {
  const row = el("div", { className: "lrow" }, [
    el("span", { className: "chip " + (n.tier === "user" ? "info" : "amb"), textContent: n.tier }),
    el("span", { className: "mono strong", textContent: n.name }),
    el("span", { className: "dim", textContent: n.hook || "" }),
    el("span", { className: "path", textContent: fmtWhen(n.updated) }),
  ]);
  row.style.cursor = "pointer";
  row.addEventListener("click", () => openNote(n.tier, n.name));
  list.appendChild(row);
}

// renderActivity appends the learn-activity strip: GET /api/loops filtered
// to kind:"learn" firings' most recent runs. Notes-written counts already
// live inside each run's free-text Reason (LearnResult.Report(), learn.go)
// and are shown verbatim rather than parsed into a new field — degrades to
// a plain outcome/time strip if that text ever changes shape, per the
// "don't invent fields another agent may be adding" constraint.
function renderActivity(container) {
  const box = el("div", { className: "lbox mem-activity" }, [el("h4", { textContent: "Recent learning activity" })]);
  apiFetch("/api/loops")
    .then((resp) => (resp.ok ? resp.json() : { loops: [] }))
    .then((vm) => {
      const runs = [];
      for (const loop of vm.loops || []) {
        if (loop.kind !== "learn") continue;
        for (const r of loop.runs || []) runs.push({ loop, r });
      }
      runs.sort((a, b) => new Date(b.r.timestamp) - new Date(a.r.timestamp));
      const rows = runs
        .slice(0, 8)
        .map(({ loop, r }) =>
          el("div", { className: "lrow" }, [
            el("span", { className: "mono", textContent: loop.name }),
            el("span", { className: "chip " + (r.outcome === "ok" ? "ok" : "bad"), textContent: r.outcome }),
            el("span", { className: "dim", textContent: r.reason || "" }),
            el("span", { className: "path", textContent: fmtWhen(r.timestamp) }),
          ]),
        );
      box.append(...(rows.length ? rows : [el("div", { className: "lrow" }, [el("span", { className: "dim", textContent: "No learn loops have run yet." })])]));
      container.appendChild(box);
    })
    .catch(() => {
      container.appendChild(box);
    });
}

// renderPicker appends a project <select> (GET /api/projects, the same
// registry listing the dashboard screen reads); changing it re-navigates
// with ?screen=memory&project=<name> so the choice survives a reload.
function renderPicker(container, projects) {
  const select = document.createElement("select");
  for (const p of projects) {
    const opt = document.createElement("option");
    opt.value = p.name;
    opt.textContent = p.name;
    select.appendChild(opt);
  }
  select.value = memState.project;
  select.addEventListener("change", () => {
    window.location.href = "?screen=memory&project=" + encodeURIComponent(select.value);
  });
  container.appendChild(el("div", { className: "mem-picker" }, [el("span", { className: "mono dim", textContent: "project:" }), select]));
}

// loadMemory no-ops when the #memory container is absent/hidden.
function loadMemory() {
  const container = document.getElementById("memory");
  if (!container) {
    return;
  }
  container.textContent = "Loading memory…";
  apiFetch("/api/projects")
    .then((resp) => (resp.ok ? resp.json() : []))
    .then((projects) => {
      container.textContent = "";
      if (!projects.length) {
        container.appendChild(el("p", { className: "empty-note", textContent: "No registered projects yet." }));
        return;
      }
      if (!projects.some((p) => p.name === memState.project)) {
        memState.project = projects[0].name;
      }
      renderPicker(container, projects);

      apiFetch("/api/memory?project=" + encodeURIComponent(memState.project))
        .then((resp) => {
          if (!resp.ok) throw new Error("GET /api/memory: " + resp.status);
          return resp.json();
        })
        .then((vm) => {
          memState.notes = vm.notes || [];
          const list = el("div", { className: "lbox" }, [el("h4", { textContent: "Notes — " + memState.project })]);
          if (memState.notes.length === 0) {
            list.appendChild(el("div", { className: "lrow" }, [el("span", { className: "dim", textContent: "No notes in either tier yet." })]));
          } else {
            for (const n of memState.notes) renderNoteRow(list, n);
          }
          container.appendChild(list);
          container.appendChild(el("div", { id: "mem-note-view" }));
          renderActivity(container);
        })
        .catch((err) => {
          container.appendChild(el("p", { className: "load-note", textContent: "Failed to load memory: " + err.message }));
        });
    })
    .catch((err) => {
      container.textContent = "Failed to load projects: " + err.message;
    });
}

loadMemory();
