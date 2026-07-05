package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/dereksantos/cortex/internal/memory"
	"github.com/dereksantos/cortex/internal/shellrisk"
	"github.com/dereksantos/cortex/internal/tools"
	"github.com/dereksantos/cortex/pkg/llm"
)

const studyFallbackWindow = 8192

var learnedWindows = map[string]int{}

var ctxSizeRe = regexp.MustCompile(`context size \((\d+) tokens\)`)

func parseCtxSize(s string) int {
	if m := ctxSizeRe.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

func (cs *CortexSession) studyWindow() int {
	if v := os.Getenv("CORTEX_LOOP_STUDY_WINDOW"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	if w, ok := learnedWindows[cs.Study.Model]; ok {
		return w
	}
	if cs.Study.Window > 0 {
		return cs.Study.Window
	}
	return studyFallbackWindow
}

var toolSet = tools.All

type ToolCall = tools.ToolCall
type FunctionCall = tools.FunctionCall

var parseXMLToolCalls = tools.ParseXMLToolCalls
var stripToolMarkup = tools.StripToolMarkup

func (cs *CortexSession) newStudyProvider(maxTokens int) *llm.OpenAICompatClient {
	base := strings.TrimRight(cs.Study.Endpoint, "/")
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	p := llm.NewOpenAICompatClient(llm.EndpointConfig{
		Name:               "study",
		BaseURL:            base,
		APIKey:             resolveKey(cs.Study),
		ChatTemplateKwargs: cs.Study.TemplateKwargs(),
		Timeout:            10 * time.Minute,
	})
	p.SetModel(cs.Study.Model)
	p.SetTemperature(cs.Study.temperature(defaultTemperature))
	p.SetMaxTokens(maxTokens)
	return p
}

func (cs *CortexSession) reasoner() *llm.OpenAICompatClient {
	base := strings.TrimRight(cs.Study.Endpoint, "/")
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	p := llm.NewOpenAICompatClient(llm.EndpointConfig{
		Name:               "shell-classifier",
		BaseURL:            base,
		APIKey:             resolveKey(cs.Study),
		ChatTemplateKwargs: cs.Study.TemplateKwargs(),
		Timeout:            10 * time.Minute,
	})
	p.SetModel(cs.Study.Model)
	p.SetTemperature(cs.Study.temperature(defaultTemperature))
	return p
}

func (cs *CortexSession) GateShell(ctx context.Context, command string) (string, bool) {
	return cs.gateShell(ctx, command)
}

func (cs *CortexSession) AllowDelete() (string, bool) { return cs.deleteRoot, cs.allowDelete }

func (cs *CortexSession) Quiet() bool { return cs.quiet }

// citationRe parses the outline citation coordinate: @session/<id>#m<start>-<end>,
// a half-open message-index range into that session transcript.
var citationRe = regexp.MustCompile(`^@session/([A-Za-z0-9-]+)#m(\d+)-(\d+)$`)

const memUnavailable = "memory is unavailable in this session (no .cortex workspace)"

func (cs *CortexSession) MemoryWrite(name, content string) (string, error) {
	if cs.memory == nil {
		return memUnavailable, nil
	}
	saved, err := cs.memory.Write(name, content, time.Now())
	if err != nil {
		return "", err
	}
	cs.captures++
	return fmt.Sprintf("saved note %q", saved), nil
}

func (cs *CortexSession) MemoryRead(name string) (string, error) {
	if cs.memory == nil {
		return memUnavailable, nil
	}
	body, err := cs.memory.Read(name)
	if os.IsNotExist(err) {
		return fmt.Sprintf("no note named %q (check the memory index, or memory_search for it)", name), nil
	}
	if err != nil {
		return "", err
	}
	return body, nil
}

func (cs *CortexSession) MemorySearch(query string) (string, error) {
	if cs.memory == nil {
		return memUnavailable, nil
	}
	hits, err := cs.memory.Search(query)
	if err != nil {
		return "", err
	}
	if len(hits) == 0 {
		return fmt.Sprintf("no notes match %q", query), nil
	}
	return renderMemoryHits(hits), nil
}

func (cs *CortexSession) MemoryForget(name string) (string, error) {
	if cs.memory == nil {
		return memUnavailable, nil
	}
	removed, err := cs.memory.Forget(name)
	if err != nil {
		return "", err
	}
	if !removed {
		return fmt.Sprintf("no note named %q to forget", name), nil
	}
	return fmt.Sprintf("forgot note %q", name), nil
}

const memoryIndexCap = 4000

func (cs *CortexSession) memoryIndexNote() string {
	if cs.memory == nil {
		return ""
	}
	idx, err := cs.memory.Index()
	if err != nil || strings.TrimSpace(idx) == "" {
		return ""
	}
	if len(idx) > memoryIndexCap {
		idx = idx[:memoryIndexCap] + "\n… (index truncated; memory_search to find the rest)"
	}
	return "These are notes you saved in earlier sessions. Read the relevant ones with " +
		"memory_read before answering; update them with memory_write when something changes.\n\n" + idx
}

func renderMemoryHits(hits []memory.NoteMeta) string {
	var b strings.Builder
	for _, m := range hits {
		when := ""
		if !m.Updated.IsZero() {
			when = " (updated " + m.Updated.UTC().Format("2006-01-02") + ")"
		}
		fmt.Fprintf(&b, "- %s — %s%s\n", m.Name, m.Hook, when)
	}
	return strings.TrimRight(b.String(), "\n")
}

// Recall resolves an outline citation to the verbatim transcript messages it
// stands for — the recovery path that makes demotion lossless
// (docs/context-architecture.md). Deterministic: no model call.
func (cs *CortexSession) Recall(citation string) (string, error) {
	// Outline renders citations in brackets (e.g., "[@session/...]"), so strip them.
	trimmed := strings.Trim(strings.TrimSpace(citation), "[]")
	m := citationRe.FindStringSubmatch(trimmed)
	if m == nil {
		return fmt.Sprintf("unrecognized citation %q (expected @session/<id>#m<start>-<end>, as shown in the session outline)", citation), nil
	}

	id := m[1]
	start, err := strconv.Atoi(m[2])
	if err != nil {
		return "", fmt.Errorf("recall %s: invalid start index: %w", citation, err)
	}
	end, err := strconv.Atoi(m[3])
	if err != nil {
		return "", fmt.Errorf("recall %s: invalid end index: %w", citation, err)
	}

	msgs, _, err := loadTranscript(filepath.Join(sessionsDir(), id+".jsonl"))
	if err != nil {
		return "", fmt.Errorf("recall %s: %w", citation, err)
	}

	if start < 0 || end > len(msgs) || start >= end {
		return fmt.Sprintf("citation %s is out of range for that transcript (%d messages)", citation, len(msgs)), nil
	}

	var b strings.Builder
	for _, msg := range msgs[start:end] {
		b.WriteString(msg.Role)
		b.WriteString("\n")
		b.WriteString(msg.Content)
		if len(msg.ToolCalls) > 0 {
			for _, call := range msg.ToolCalls {
				b.WriteString("\n  ▸ ")
				b.WriteString(call.ActivityLabel())
			}
		}
		b.WriteString("\n\n")
	}

	rendered := b.String()
	if len(rendered)/4 > tools.CurationBudgetTokens {
		return fmt.Sprintf("recall of %s is ~%d tokens — over the %d-token curation budget. Study the transcript instead: study(%s, <your question>)", citation, len(rendered)/4, tools.CurationBudgetTokens, filepath.Join(sessionsDir(), id+".jsonl")), nil
	}

	return rendered, nil
}

func (cs *CortexSession) gateShell(ctx context.Context, command string) (string, bool) {
	var fn shellrisk.ClassifyFn
	if cs != nil {
		fn = cs.classifyShell
		if fn == nil {
			fn = func(ctx context.Context, command string) (shellrisk.Level, string, error) {
				return shellrisk.ProviderClassifier(cs.reasoner(), cs.turnIntent)(ctx, command)
			}
		}
	}
	v := shellrisk.Classify(ctx, command, fn)
	switch v.Level {
	case shellrisk.Safe:
		return "", true
	case shellrisk.Blocked:
		return fmt.Sprintf("refused by the safety gate (%s). This command will not run; choose a safer approach.", v.Reason), false
	default:
		if cs != nil && !cs.quiet && cs.confirmRisky != nil {
			q := fmt.Sprintf("\n⚠ risky command — %s\n    %s\n  run it? [y/N] ", v.Reason, command)
			if cs.confirmRisky(q) {
				return "", true
			}
			return "declined by the user; not run. Ask before retrying, or use a safer command.", false
		}
		return fmt.Sprintf("blocked (risk: %s). No interactive approval is available in this session — re-issue a safer command, or ask the user to run it.", v.Reason), false
	}
}
