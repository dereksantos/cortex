package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProbeSend(t *testing.T) {
	t.Setenv("CORTEX_LOOP_STREAM", "1")
	bodyAnswer := `{"choices":[{"delta":{"role":"assistant","content":"hello"}}]}`
	bodyFinishStop := `{"choices":[{"delta":{},"finish_reason":"stop"}]}`
	bodyUsage := `{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(sseBody(bodyAnswer, bodyFinishStop, bodyUsage)))
	}))
	defer srv.Close()
	cs := &CortexSession{Request: &AgentRequest{Model: "m", BaseURL: srv.URL, Messages: []Message{{Role: RoleSystem, Content: "s"}}}}
	res, streamed, err := cs.send(context.Background())
	t.Logf("send err=%v res=%v streamed=%v", err, res != nil, streamed)
	fmt.Println("done")
}
