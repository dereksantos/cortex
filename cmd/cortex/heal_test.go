package main

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/dereksantos/cortex/pkg/llm"
)

// healSession builds the minimal session the healing ladder touches: an
// OpenRouter config, a live coder request, a study binding sharing the same
// model (the common default), and a fake catalog.
func healSession(model string, served []llm.OpenRouterModel, listErr error) *CortexSession {
	return &CortexSession{
		Config:  &Config{Backend: Backend{Type: "openrouter"}},
		Request: &AgentRequest{Model: model},
		Study:   ModelSpec{Model: model},
		healList: func(ctx context.Context) ([]llm.OpenRouterModel, error) {
			return served, listErr
		},
	}
}

// servedCurated returns a catalog serving the first n curated entries.
func servedCurated(n int) []llm.OpenRouterModel {
	out := make([]llm.OpenRouterModel, 0, n)
	for _, m := range curatedFreeModels[:n] {
		out = append(out, llm.OpenRouterModel{ID: m.ID, ContextLength: m.Window})
	}
	return out
}

// failNTimes is an inner Sender failing with err for the first n calls, then
// answering ok. It records every model it was asked to send as.
func failNTimes(n int, err error, models *[]string) Sender {
	calls := 0
	return SenderFunc(func(_ context.Context, req *AgentRequest) (*AgentResponse, bool, error) {
		*models = append(*models, req.Model)
		calls++
		if calls <= n {
			return nil, false, err
		}
		return &AgentResponse{Choices: []Choice{{Message: Message{Role: "assistant", Content: "ok"}}}}, false, nil
	})
}

func TestHealingSenderSwapsOnModelMissing(t *testing.T) {
	cs := healSession("pinned/gone", servedCurated(2), nil)
	var models []string
	cause := &modelCallError{Status: 404, Class: classModelMissing, Model: "pinned/gone", Detail: "no endpoints"}
	s := cs.healingSender(roleCode, failNTimes(1, cause, &models))

	res, _, err := s.Send(context.Background(), cs.Request)
	if err != nil {
		t.Fatalf("expected healed success, got %v", err)
	}
	if res == nil || len(res.Choices) == 0 {
		t.Fatal("expected a response from the healed model")
	}
	want := curatedFreeModels[0].ID
	if cs.Request.Model != want {
		t.Errorf("coder request model = %q, want healed %q", cs.Request.Model, want)
	}
	if cs.Study.Model != want {
		t.Errorf("study binding = %q, want healed %q (shared the dead model)", cs.Study.Model, want)
	}
	if cs.Window != curatedFreeModels[0].Window {
		t.Errorf("window = %d, want curated %d", cs.Window, curatedFreeModels[0].Window)
	}
	if len(models) != 2 || models[0] != "pinned/gone" || models[1] != want {
		t.Errorf("send sequence = %v, want [pinned/gone %s]", models, want)
	}
	if _, dead := cs.deadModels["pinned/gone"]; !dead {
		t.Error("failing model should be marked session-dead")
	}
}

func TestHealingSenderWalksPastDeadCandidates(t *testing.T) {
	cs := healSession("pinned/gone", servedCurated(3), nil)
	var models []string
	cause := &modelCallError{Status: 429, Class: classRateLimited, Model: "pinned/gone", Detail: "slow down"}
	// Original + first candidate fail, second candidate succeeds.
	s := cs.healingSender(roleCode, failNTimes(2, cause, &models))

	if _, _, err := s.Send(context.Background(), cs.Request); err != nil {
		t.Fatalf("expected healed success, got %v", err)
	}
	want := curatedFreeModels[1].ID
	if cs.Request.Model != want {
		t.Errorf("model = %q, want second curated %q", cs.Request.Model, want)
	}
	if _, dead := cs.deadModels[curatedFreeModels[0].ID]; !dead {
		t.Error("failed first candidate should be marked dead")
	}
}

func TestHealingSenderNeverSwapsOnAuth(t *testing.T) {
	cs := healSession("pinned/x", servedCurated(2), nil)
	var models []string
	cause := &modelCallError{Status: 401, Class: classAuth, Model: "pinned/x", Detail: "bad key"}
	s := cs.healingSender(roleCode, failNTimes(99, cause, &models))

	_, _, err := s.Send(context.Background(), cs.Request)
	if err == nil {
		t.Fatal("expected the auth error to surface")
	}
	if cs.Request.Model != "pinned/x" {
		t.Errorf("model changed to %q — auth failures must never swap", cs.Request.Model)
	}
	if len(models) != 1 {
		t.Errorf("expected exactly one send, got %d", len(models))
	}
	if len(cs.deadModels) != 0 {
		t.Errorf("no model should be marked dead on auth, got %v", cs.deadModels)
	}
}

func TestHealingSenderCatalogDownReturnsOriginalError(t *testing.T) {
	cs := healSession("pinned/gone", nil, errors.New("catalog unreachable"))
	var models []string
	cause := &modelCallError{Status: 404, Class: classModelMissing, Model: "pinned/gone"}
	s := cs.healingSender(roleCode, failNTimes(99, cause, &models))

	_, _, err := s.Send(context.Background(), cs.Request)
	var mce *modelCallError
	if !errors.As(err, &mce) || mce.Status != 404 {
		t.Fatalf("expected original 404 to surface, got %v", err)
	}
	if cs.Request.Model != "pinned/gone" {
		t.Errorf("model changed to %q despite catalog being down", cs.Request.Model)
	}
}

func TestHealingSenderGateOff(t *testing.T) {
	cs := healSession("pinned/gone", servedCurated(2), nil)
	off := false
	cs.Config.Network.SelfHeal = &off
	var models []string
	cause := &modelCallError{Status: 404, Class: classModelMissing, Model: "pinned/gone"}
	s := cs.healingSender(roleCode, failNTimes(99, cause, &models))

	if _, _, err := s.Send(context.Background(), cs.Request); err == nil {
		t.Fatal("expected error with self_heal off")
	}
	if cs.Request.Model != "pinned/gone" || len(models) != 1 {
		t.Errorf("self_heal:false must not swap (model=%q, sends=%d)", cs.Request.Model, len(models))
	}
}

func TestHealingSenderNonOpenRouterNeverHeals(t *testing.T) {
	cs := healSession("local-model", servedCurated(2), nil)
	cs.Config.Backend.Type = "litellm"
	var models []string
	cause := &modelCallError{Status: 404, Class: classModelMissing, Model: "local-model"}
	s := cs.healingSender(roleCode, failNTimes(99, cause, &models))

	if _, _, err := s.Send(context.Background(), cs.Request); err == nil {
		t.Fatal("expected error on non-OpenRouter backend")
	}
	if cs.Request.Model != "local-model" || len(models) != 1 {
		t.Errorf("non-OpenRouter must not ladder (model=%q, sends=%d)", cs.Request.Model, len(models))
	}
}

func TestHealingSenderExhaustionSurfacesOriginalError(t *testing.T) {
	cs := healSession("pinned/gone", servedCurated(len(curatedFreeModels)), nil)
	var models []string
	cause := &modelCallError{Status: 503, Class: classServer, Model: "pinned/gone", Detail: "down"}
	s := cs.healingSender(roleCode, failNTimes(99, cause, &models))

	_, _, err := s.Send(context.Background(), cs.Request)
	var mce *modelCallError
	if !errors.As(err, &mce) || mce.Status != 503 {
		t.Fatalf("expected original 503 after exhaustion, got %v", err)
	}
	// Original send + at most healMaxCandidates candidate sends.
	if len(models) != 1+healMaxCandidates {
		t.Errorf("sends = %d, want %d (1 original + %d candidates)", len(models), 1+healMaxCandidates, healMaxCandidates)
	}
}

func TestHealingSenderStreamedErrorPassesThrough(t *testing.T) {
	cs := healSession("pinned/x", servedCurated(2), nil)
	cause := errors.New("stream status 503: mid-stream failure")
	inner := SenderFunc(func(_ context.Context, _ *AgentRequest) (*AgentResponse, bool, error) {
		return nil, true, cause // partial echo already happened
	})
	s := cs.healingSender(roleCode, inner)

	_, streamed, err := s.Send(context.Background(), cs.Request)
	if !streamed || !errors.Is(err, cause) {
		t.Fatalf("streamed partial failure must pass through, got streamed=%v err=%v", streamed, err)
	}
	if cs.Request.Model != "pinned/x" {
		t.Errorf("model changed to %q after a partially-streamed reply", cs.Request.Model)
	}
}

func TestHealingSenderDiscoveryFallbackWhenNoCuratedServed(t *testing.T) {
	served := []llm.OpenRouterModel{
		{ID: "someone/random-chat:free", ContextLength: 64000},
		{ID: "someone/big-coder:free", ContextLength: 128000},
	}
	cs := healSession("pinned/gone", served, nil)
	var models []string
	cause := &modelCallError{Status: 404, Class: classModelMissing, Model: "pinned/gone"}
	s := cs.healingSender(roleCode, failNTimes(1, cause, &models))

	if _, _, err := s.Send(context.Background(), cs.Request); err != nil {
		t.Fatalf("expected discovery fallback to heal, got %v", err)
	}
	if cs.Request.Model != "someone/big-coder:free" {
		t.Errorf("model = %q, want coder-named discovery pick", cs.Request.Model)
	}
}

func TestHealingSenderUserCancelNeverHeals(t *testing.T) {
	cs := healSession("pinned/x", servedCurated(2), nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	inner := SenderFunc(func(_ context.Context, _ *AgentRequest) (*AgentResponse, bool, error) {
		return nil, false, fmt.Errorf("send: %w", context.Canceled)
	})
	s := cs.healingSender(roleCode, inner)

	if _, _, err := s.Send(ctx, cs.Request); err == nil {
		t.Fatal("expected the cancel to surface")
	}
	if cs.Request.Model != "pinned/x" {
		t.Errorf("model changed to %q on user cancel", cs.Request.Model)
	}
}
