// sse.js — M5.3c4: a small, reusable Server-Sent-Event frame parser for
// consuming a fetch() response's streamed body. EventSource can't be used
// here — it's GET-only, and POST .../turn/stream (serve_stream.go) needs a
// request body carrying the turn's input text — so this reads the stream
// via response.body.getReader() instead, decodes UTF-8 chunks, and splits
// on the blank-line ("\n\n") frame boundary the SSE wire format uses
// (matching serve_stream.go's sseEvent: "event: <name>\ndata: <json>\n\n").
//
// streamSSE(response, handlers) calls handlers[<event name>](<parsed JSON
// payload>) once per complete frame, in arrival order, and resolves its
// returned promise once the stream ends.
function streamSSE(response, handlers) {
  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";

  function handleFrame(frame) {
    let event = "message";
    let data = "";
    for (const line of frame.split("\n")) {
      if (line.startsWith("event: ")) {
        event = line.slice("event: ".length);
      } else if (line.startsWith("data: ")) {
        data = line.slice("data: ".length);
      }
    }
    const handler = handlers[event];
    if (handler && data) {
      handler(JSON.parse(data));
    }
  }

  function pump() {
    return reader.read().then(({ done, value }) => {
      if (value) {
        buffer += decoder.decode(value, { stream: true });
      }
      let sep;
      while ((sep = buffer.indexOf("\n\n")) !== -1) {
        handleFrame(buffer.slice(0, sep));
        buffer = buffer.slice(sep + 2);
      }
      if (done) {
        return;
      }
      return pump();
    });
  }

  return pump();
}
