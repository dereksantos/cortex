// Package prompts holds the versioned prompt templates for each
// LLM-backed DAG op registered in pkg/cognition/dag/ops.
//
// Convention per ADR-004:
//
//   - One .tmpl file per op, named <function>_<op>.tmpl.
//   - YAML frontmatter at the top, delimited by `---` lines, carries
//     metadata: version, op, description, max_output_tokens, vars.
//   - Body is a Go text/template. Variables referenced in the body
//     must be declared in frontmatter.vars (loader enforces this).
//   - Output contract is part of the prompt body: tell the LLM the
//     exact JSON shape expected. Each op handler parses + validates.
//
// Templates are bundled into the binary via embed.FS — no runtime
// file system dependency, so ops work the same in tests, dev, and
// `cortex install`-ed binaries.
package prompts

import "embed"

// FS is the embedded prompt template tree. Loaded lazily by
// pkg/cognition/dag/ops/template.go.
//
//go:embed *.tmpl
var FS embed.FS
