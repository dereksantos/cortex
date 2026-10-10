// attach.js — the turn composer's image attachments (issue #218 step 6).
//
// Two ways to attach, both sent in the POST turn/stream body's `attachments`
// (serve_attachments.go, step 5): a file from an <input type="file">, read
// here into a base64 `data:` URI under `{data}` (a browser-picked file has no
// workspace path, so the bytes are the only thing that can travel), and an
// image URL sent as `{url}` for the SERVER to fetch through its SSRF-guarded
// public fetch rather than whatever the browser got.
//
// A picked file is vetted before sending, in the composer's status line, using
// the same four extensions and the same 1.5 MB ceiling the server enforces —
// not to make the server safe (it re-reads and re-judges every attachment and
// is the authority) but so a stray .pdf fails in the composer instead of as an
// opaque 400 later.
//
// Its own file because the per-file JS cap is 300 lines (webui_jscap_test.go)
// and app.js's composer is where the call lands. Like session.js it is a plain
// global loaded after app.js and reuses its el().

// The four formats Cortex accepts, matching the server's sniffer, and the
// tools.read.image_max_bytes ceiling it enforces.
var ATTACH_IMAGE_EXT = ["png", "jpg", "jpeg", "gif", "webp"];
var ATTACH_IMAGE_MAX_BYTES = 1500000;

// attachFileError returns why this file cannot be attached, or "" if it can.
function attachFileError(file) {
  if (!file) {
    return "no file selected";
  }
  const name = (file.name || "").toLowerCase();
  const dot = name.lastIndexOf(".");
  const ext = dot < 0 ? "" : name.slice(dot + 1);
  if (ATTACH_IMAGE_EXT.indexOf(ext) < 0) {
    return name + " is not a supported image (png, jpg, jpeg, gif, webp)";
  }
  if (file.size > ATTACH_IMAGE_MAX_BYTES) {
    return name + " is over the " + Math.round(ATTACH_IMAGE_MAX_BYTES / 1000) + " KB image limit";
  }
  return "";
}

// readAttachmentFile reads a vetted File into a `data:` URI. Resolves
// {attachment, error} and never throws, so a failed read lands in the status
// line instead of dying silently. An empty payload is a failure, not an empty
// image.
function readAttachmentFile(file) {
  return new Promise(function (resolve) {
    const why = attachFileError(file);
    if (why) {
      resolve({ attachment: null, error: why });
      return;
    }
    const reader = new FileReader();
    reader.onload = function () {
      const uri = String(reader.result || "");
      if (uri.indexOf("data:") !== 0 || uri.indexOf(";base64,") < 0) {
        resolve({ attachment: null, error: (file.name || "file") + " could not be read" });
        return;
      }
      resolve({ attachment: { data: uri, name: file.name || "image" }, error: "" });
    };
    reader.onerror = function () {
      resolve({ attachment: null, error: (file.name || "file") + " could not be read" });
    };
    reader.readAsDataURL(file);
  });
}

// attachmentSummaryLine is the visible attachment line for a pending pick, so
// what is about to be sent is legible before it is sent.
function attachmentSummaryLine(att) {
  if (!att) {
    return "";
  }
  if (att.url) {
    return "attached image: " + att.url;
  }
  return "attached image: " + att.name + (att.bytes ? " (" + Math.round(att.bytes / 1000) + " KB)" : "");
}

// turnAttachments builds the body's `attachments` array: the picked file as
// base64 {data, name}, and the URL field as {url} for the server to fetch.
function turnAttachments(pending, urlValue) {
  const out = [];
  if (pending && pending.data) {
    out.push({ data: pending.data, name: pending.name || "attached image" });
  }
  const url = (urlValue || "").trim();
  if (url) {
    out.push({ url: url });
  }
  return out;
}

// renderComposerAttachments appends the picker row to the composer form and
// returns {pending, urlInput, collect, clear} for the submit handler. A
// rejected file is named in `status` and the field cleared, leaving the text
// field exactly as the coder left it.
function renderComposerAttachments(form, status) {
  const pending = { att: null };
  const pick = el("input", { type: "file", id: "turn-attach-file", accept: ATTACH_IMAGE_EXT.map(function (x) { return "." + x; }).join(",") });
  const urlInput = el("input", { type: "text", id: "turn-attach-url", placeholder: "or an image URL" });
  const line = el("span", { className: "attach-line", id: "turn-attach-line" });

  const reject = function (why) {
    status.textContent = why;
    pick.value = "";
    pending.att = null;
    line.textContent = "";
  };

  pick.addEventListener("change", function () {
    const file = pick.files && pick.files[0];
    if (!file) {
      pending.att = null;
      line.textContent = "";
      return;
    }
    readAttachmentFile(file).then(function (r) {
      if (r.error) {
        reject(r.error);
        return;
      }
      status.textContent = "";
      pending.att = r.attachment;
      line.textContent = attachmentSummaryLine({ name: r.attachment.name, bytes: file.size });
    });
  });

  form.appendChild(el("div", { className: "composer-attach" }, [pick, urlInput, line]));

  return {
    pending: pending,
    urlInput: urlInput,
    // collect returns {attachments, error}: a file present but unread at submit
    // must surface as a status line, not a turn silently missing its image.
    collect: function () {
      if (pick.files && pick.files.length > 0 && !pending.att) {
        return { attachments: turnAttachments(null, urlInput.value), error: "the selected image is not ready yet" };
      }
      return { attachments: turnAttachments(pending.att, urlInput.value), error: "" };
    },
    // clear drops the pending attachment after a send, so the same screenshot
    // is not silently re-sent on the next turn.
    clear: function () {
      pending.att = null;
      pick.value = "";
      urlInput.value = "";
      line.textContent = "";
    },
  };
}
