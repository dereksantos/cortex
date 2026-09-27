package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// budget.go holds the run's two money-and-secrets concerns:
//
//   - the spend guard: OpenRouter's /api/v1/key reports the key's cumulative
//     usage in USD. The driver reads it before the run, between instances,
//     and on a timer during each turn, and interrupts the turn (or declines to
//     start the next instance) before the run can exceed its cap. This is
//     independent of cortex's own accounting, so a cortex bug that
//     under-counts cost cannot blow the budget.
//   - the key itself: read once from the macOS keychain into memory, handed
//     to the in-container cortex process only through the docker CLI's
//     process environment (never argv, never a file, never a log), and
//     scrubbed from every artifact before the instance is recorded.

// UsageReader reports the account's cumulative spend in USD.
type UsageReader interface {
	Usage(ctx context.Context) (float64, error)
}

type openRouterUsage struct {
	url    string
	key    string
	client *http.Client
}

func newOpenRouterUsage(baseURL, key string) *openRouterUsage {
	return &openRouterUsage{
		url:    strings.TrimRight(baseURL, "/") + "/key",
		key:    key,
		client: &http.Client{Timeout: 20 * time.Second},
	}
}

type keyInfo struct {
	Data struct {
		Usage float64 `json:"usage"`
	} `json:"data"`
}

func (u *openRouterUsage) Usage(ctx context.Context) (float64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.url, nil)
	if err != nil {
		return 0, fmt.Errorf("failed to build usage request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+u.key)
	resp, err := u.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("failed to read account usage: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		// Never echo the body wholesale: keep it short and key-free.
		return 0, fmt.Errorf("account usage: HTTP %d", resp.StatusCode)
	}
	var ki keyInfo
	if err := json.Unmarshal(body, &ki); err != nil {
		return 0, fmt.Errorf("failed to parse account usage: %w", err)
	}
	return ki.Data.Usage, nil
}

// readKeychain returns the secret stored under service. It is never logged.
func readKeychain(service string) (string, error) {
	out, err := exec.Command("security", "find-generic-password", "-s", service, "-w").Output()
	if err != nil {
		return "", fmt.Errorf("failed to read keychain service %q: %w", service, err)
	}
	key := strings.TrimSpace(string(out))
	if key == "" {
		return "", fmt.Errorf("keychain service %q is empty", service)
	}
	return key, nil
}

// BudgetGuard decides whether the next instance may start.
type BudgetGuard struct {
	Budget      float64 // total cap for the run, USD
	InstanceCap float64 // per-instance cap, USD (the turn is interrupted at it)
	EstFirst    float64 // assumed cost of an instance before any is observed
	observedMax float64
}

// Observe records one finished instance's spend.
func (g *BudgetGuard) Observe(cost float64) {
	if cost > g.observedMax {
		g.observedMax = cost
	}
}

// NextEstimate is the spend the next instance is assumed to need: the most
// expensive instance so far (or EstFirst before any), never more than the
// per-instance cap that the in-turn guard enforces anyway.
func (g *BudgetGuard) NextEstimate() float64 {
	est := g.observedMax
	if est == 0 {
		est = g.EstFirst
	}
	if g.InstanceCap > 0 && est > g.InstanceCap {
		est = g.InstanceCap
	}
	return est
}

// CanStart reports whether an instance may start given what the run has
// spent so far.
func (g *BudgetGuard) CanStart(spent float64) bool {
	return spent+g.NextEstimate() <= g.Budget
}

// InstanceLimit is how much this instance may spend before the in-turn
// guard interrupts it: the per-instance cap, further clipped so the run as a
// whole never passes Budget.
func (g *BudgetGuard) InstanceLimit(spentBefore float64) float64 {
	limit := g.Budget - spentBefore
	if g.InstanceCap > 0 && g.InstanceCap < limit {
		limit = g.InstanceCap
	}
	if limit < 0 {
		return 0
	}
	return limit
}

// redactTree replaces every occurrence of secret in regular files under
// root. It reports whether anything was found. The key should never appear
// in an artifact (cortex strips key_env vars from its shell); this is the
// backstop, and a true return is a finding worth investigating.
func redactTree(root, secret string) (bool, error) {
	if secret == "" {
		return false, nil
	}
	needle := []byte(secret)
	found := false
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if !bytes.Contains(b, needle) {
			return nil
		}
		found = true
		info, _ := d.Info()
		mode := fs.FileMode(0o644)
		if info != nil {
			mode = info.Mode().Perm()
		}
		return os.WriteFile(path, bytes.ReplaceAll(b, needle, []byte("[REDACTED-API-KEY]")), mode)
	})
	return found, err
}
