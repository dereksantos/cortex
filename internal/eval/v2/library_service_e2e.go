//go:build !windows

package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// endToEndPassRate builds workdir's cmd/server, starts it on a random free
// port, and exercises every endpoint defined in the rubric. Returns the
// fraction of probes whose response status class matched expectations.
//
// Failure semantics intentionally split:
//   - missing cmd/server  -> (0, nil): nothing to score, no error
//   - build fails         -> (0, err): error wraps the build output
//   - server fails to come up after build -> (0, err): error includes captured
//     stdout/stderr so eval operators can debug. Score swallows this error,
//     so the metric still records 0 cleanly without aborting the rubric.
func endToEndPassRate(workdir string) (float64, error) {
	mainPath := filepath.Join(workdir, "cmd", "server", "main.go")
	if _, err := os.Stat(mainPath); err != nil {
		return 0, nil
	}

	bin, cleanup, err := buildLibraryServer(workdir)
	if err != nil {
		return 0, err
	}
	defer cleanup()

	port, err := pickFreePort()
	if err != nil {
		return 0, fmt.Errorf("pick port: %w", err)
	}

	sub, err := startLibraryServer(bin, port)
	if err != nil {
		return 0, err
	}
	defer sub.Stop()

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	if !sub.WaitReady(base, 5*time.Second) {
		// Reap before reading the output buffer — Stop blocks on the wait
		// goroutine, after which no more writes can race.
		sub.Stop()
		return 0, fmt.Errorf("server did not come up on port %d within 5s; output:\n%s",
			port, sub.LastOutput())
	}

	return runEndpointProbes(base), nil
}

func buildLibraryServer(workdir string) (string, func(), error) {
	// Build into a tempdir we own outright — keeps workdir clean and avoids
	// stomping on anything the workdir might already contain (e.g., a bin/
	// directory the model produced for its own tests).
	binDir, err := os.MkdirTemp("", "library-service-e2e-bin-*")
	if err != nil {
		return "", func() {}, fmt.Errorf("bin dir: %w", err)
	}
	bin := filepath.Join(binDir, "server")

	cmd := exec.Command("go", "build", "-o", bin, "./cmd/server")
	cmd.Dir = workdir
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		_ = os.RemoveAll(binDir)
		return "", func() {}, fmt.Errorf("build failed: %w: %s", err, buf.String())
	}

	cleanup := func() { _ = os.RemoveAll(binDir) }
	return bin, cleanup, nil
}

func pickFreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port, nil
}

// libraryServerProcess wraps the running server subprocess with reliable
// cleanup. WaitReady polls the server while watching for early exit so a
// crashed server is detected without burning the full timeout.
type libraryServerProcess struct {
	cmd      *exec.Cmd
	output   *bytes.Buffer
	waitErr  chan error // closed when Wait returns
	stopOnce sync.Once
}

func startLibraryServer(bin string, port int) (*libraryServerProcess, error) {
	dataDir, err := os.MkdirTemp("", "library-service-e2e-data-*")
	if err != nil {
		return nil, fmt.Errorf("data dir: %w", err)
	}

	cmd := exec.Command(bin)
	cmd.Dir = dataDir // any sqlite/temp files land here, isolated per run
	cmd.Env = append(os.Environ(), fmt.Sprintf("PORT=%d", port))
	// Process group so SIGTERM reaches the binary even if it spawns children.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(dataDir)
		return nil, fmt.Errorf("start server: %w", err)
	}

	sub := &libraryServerProcess{
		cmd:     cmd,
		output:  &buf,
		waitErr: make(chan error, 1),
	}

	go func() {
		err := cmd.Wait()
		_ = os.RemoveAll(dataDir)
		sub.waitErr <- err
		close(sub.waitErr)
	}()
	return sub, nil
}

// WaitReady polls the server's listening port. It also watches the process
// state so an early exit (e.g. empty main returning immediately) short-circuits
// instead of waiting the full timeout.
func (s *libraryServerProcess) WaitReady(base string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	probeURL := base + "/"
	client := &http.Client{Timeout: 250 * time.Millisecond}

	for time.Now().Before(deadline) {
		select {
		case <-s.waitErr:
			return false
		default:
		}
		resp, err := client.Get(probeURL)
		if err == nil {
			_ = resp.Body.Close()
			return true
		}
		select {
		case <-s.waitErr:
			return false
		case <-time.After(100 * time.Millisecond):
		}
	}
	return false
}

func (s *libraryServerProcess) Stop() {
	s.stopOnce.Do(func() {
		if s.cmd.Process == nil {
			return
		}
		// SIGTERM first; fall back to SIGKILL if the process ignores it.
		_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-s.waitErr:
		case <-time.After(2 * time.Second):
			_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL)
			<-s.waitErr
		}
	})
}

// LastOutput returns whatever the subprocess wrote to stdout/stderr.
//
// Safe to call after Stop has returned: Stop synchronizes on the Wait
// goroutine, so no more writes can happen concurrently. Calling it while
// the process is still running races with the Stdout/Stderr writes — the
// caller is responsible for stopping first.
func (s *libraryServerProcess) LastOutput() string {
	return s.output.String()
}

// endpointProbe describes one HTTP probe in the e2e plan.
//
// Path and body templates may reference captured ids via {key} placeholders;
// runEndpointProbes substitutes them in order. captureKey, when set, parses
// the response body as JSON and stashes its `id` field for later probes.
type endpointProbe struct {
	name       string
	method     string
	path       string
	body       string
	wantStatus int
	captureKey string
}

// libraryEndpointPlan returns the 25-probe plan in dependency order.
//
// All resources POST/list/get/put first (so loans has parent ids to reference),
// then DELETEs run last across the resources that loans depend on. Branches and
// loans run their DELETE inline because no resource depends on them.
func libraryEndpointPlan() []endpointProbe {
	plan := make([]endpointProbe, 0, 25)

	// Resources whose ids loans need: authors, books, members.
	// Order: 4 ops here (POST,LIST,GET,PUT), DELETE deferred.
	plan = append(plan, crudHead("authors", `{"name":"a","birth_year":1900}`, `{"name":"a2","birth_year":1901}`)...)
	plan = append(plan, crudHead("members", `{"name":"m","email":"m@example.com"}`, `{"name":"m2","email":"m2@example.com"}`)...)

	// branches has no dependents — run all 5 ops inline.
	plan = append(plan, crudFull("branches", `{"name":"b","address":"1 st"}`, `{"name":"b2","address":"2 st"}`)...)

	// books depends on authors; 4 ops here, DELETE deferred.
	plan = append(plan, crudHead("books",
		`{"title":"t","isbn":"i","author_id":"{authors_id}","published_year":2020}`,
		`{"title":"t2","isbn":"i2","author_id":"{authors_id}","published_year":2021}`)...)

	// loans depends on books + members; full 5 ops since nothing depends on it.
	plan = append(plan, crudFull("loans",
		`{"book_id":"{books_id}","member_id":"{members_id}","loaned_at":"2025-01-01T00:00:00Z"}`,
		`{"book_id":"{books_id}","member_id":"{members_id}","loaned_at":"2025-01-02T00:00:00Z"}`)...)

	// Deferred DELETEs for resources loans referenced.
	plan = append(plan,
		endpointProbe{
			name: "DELETE /authors/{id}", method: http.MethodDelete,
			path: "/authors/{authors_id}", wantStatus: http.StatusNoContent,
		},
		endpointProbe{
			name: "DELETE /members/{id}", method: http.MethodDelete,
			path: "/members/{members_id}", wantStatus: http.StatusNoContent,
		},
		endpointProbe{
			name: "DELETE /books/{id}", method: http.MethodDelete,
			path: "/books/{books_id}", wantStatus: http.StatusNoContent,
		},
	)

	return plan
}

// crudHead returns POST, GET list, GET one, PUT for a resource — DELETE is
// deferred so dependents (loans) can still reference the captured id.
func crudHead(resource, createBody, updateBody string) []endpointProbe {
	idRef := "{" + resource + "_id}"
	base := "/" + resource
	one := base + "/" + idRef
	return []endpointProbe{
		{
			name: "POST " + base, method: http.MethodPost,
			path: base, body: createBody,
			wantStatus: http.StatusCreated, captureKey: resource + "_id",
		},
		{name: "GET " + base, method: http.MethodGet, path: base, wantStatus: http.StatusOK},
		{name: "GET " + base + "/{id}", method: http.MethodGet, path: one, wantStatus: http.StatusOK},
		{
			name: "PUT " + base + "/{id}", method: http.MethodPut,
			path: one, body: updateBody, wantStatus: http.StatusNoContent,
		},
	}
}

// crudFull is crudHead plus the trailing DELETE — for resources nothing else
// depends on, so it's safe to delete inline.
func crudFull(resource, createBody, updateBody string) []endpointProbe {
	idRef := "{" + resource + "_id}"
	one := "/" + resource + "/" + idRef
	out := crudHead(resource, createBody, updateBody)
	out = append(out, endpointProbe{
		name: "DELETE /" + resource + "/{id}", method: http.MethodDelete,
		path: one, wantStatus: http.StatusNoContent,
	})
	return out
}

// runEndpointProbes executes the plan against base and returns passed/total.
// A capture failure on POST will cause subsequent dependent probes to fail
// (templated path won't substitute), which is the correct signal — partial
// implementations naturally score partial.
func runEndpointProbes(base string) float64 {
	plan := libraryEndpointPlan()
	captures := make(map[string]string)
	client := &http.Client{Timeout: 5 * time.Second}

	passed := 0
	for _, p := range plan {
		path := substituteTemplate(p.path, captures)
		body := substituteTemplate(p.body, captures)

		var bodyReader io.Reader
		if body != "" {
			bodyReader = strings.NewReader(body)
		}
		req, err := http.NewRequestWithContext(context.Background(), p.method, base+path, bodyReader)
		if err != nil {
			continue
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		if statusClass(resp.StatusCode) != statusClass(p.wantStatus) {
			continue
		}
		passed++

		if p.captureKey != "" {
			if id, ok := extractID(raw); ok {
				captures[p.captureKey] = id
			}
		}
	}

	if len(plan) == 0 {
		return 0
	}
	return float64(passed) / float64(len(plan))
}

func statusClass(code int) int { return code / 100 }

func substituteTemplate(s string, captures map[string]string) string {
	if s == "" || !strings.Contains(s, "{") {
		return s
	}
	for k, v := range captures {
		s = strings.ReplaceAll(s, "{"+k+"}", v)
	}
	return s
}

// extractID best-effort pulls an "id" field out of a JSON response. Handlers
// in the wild return either {"id": "..."} or the full record with id; both are
// fine. Anything we can't parse as an object yields ("", false).
func extractID(raw []byte) (string, bool) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return "", false
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return "", false
	}
	v, ok := obj["id"]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	if !ok {
		return "", false
	}
	return s, true
}
