package main

import (
	"context"
	"fmt"
	"os"
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
