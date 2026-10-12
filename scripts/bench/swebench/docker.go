package main

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// docker.go wraps the docker CLI calls the driver makes. Every container it
// creates is named by containerName (prefix "cortex-swebench-"), and it
// only ever removes containers it created, by name — the host may be running
// other containers that are none of this driver's business.

// dockerRun runs `docker args...` and returns trimmed stdout; stderr is folded
// into the error.
func dockerRun(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return strings.TrimSpace(stdout.String()), fmt.Errorf("docker %s: %w: %s",
			firstArgs(args), err, strings.TrimSpace(lastLines(stderr.String(), 6)))
	}
	return strings.TrimSpace(stdout.String()), nil
}

func firstArgs(args []string) string {
	if len(args) > 3 {
		return strings.Join(args[:3], " ") + " …"
	}
	return strings.Join(args, " ")
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// imagePresent reports whether image exists locally.
func imagePresent(ctx context.Context, image string) bool {
	_, err := dockerRun(ctx, "image", "inspect", "--format", "{{.Id}}", image)
	return err == nil
}

// ensureImage pulls image unless present; it reports whether it pulled.
func ensureImage(ctx context.Context, image string) (bool, time.Duration, error) {
	if imagePresent(ctx, image) {
		return false, 0, nil
	}
	start := time.Now()
	pctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	_, err := dockerRun(pctx, "pull", "--platform", "linux/amd64", image)
	return err == nil, time.Since(start), err
}

// startContainer starts a long-lived container from the instance image.
func startContainer(ctx context.Context, name, image string) error {
	// A leftover container of the same name from an interrupted run of THIS
	// run id is ours to replace.
	_, _ = dockerRun(ctx, "rm", "-f", name)
	_, err := dockerRun(ctx, "run", "-d", "--platform", "linux/amd64", "--name", name,
		"--entrypoint", "", image, "tail", "-f", "/dev/null")
	return err
}

func removeContainer(name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	_, _ = dockerRun(ctx, "rm", "-f", name)
}

// execIn runs a bash script inside the container.
func execIn(ctx context.Context, name, script string) (string, error) {
	return dockerRun(ctx, "exec", name, "bash", "-c", script)
}

// dockerServerArch is the docker server's os/arch, e.g. "linux/arm64".
func dockerServerArch(ctx context.Context) string {
	out, err := dockerRun(ctx, "version", "--format", "{{.Server.Os}}/{{.Server.Arch}}")
	if err != nil {
		return "unknown"
	}
	return out
}
