// manifest.go — generator that turns the command registry into a
// tools.json manifest (axis 1, Contract; see docs/tool-surface.md).
//
// Each registered command contributes one ToolEntry. Commands that
// implement FlagDescriber / ArgsDescriber / Versioner enrich their
// entry; others fall back to Name + Description + binary version.
//
// Determinism: the Tools slice is sorted by name and the flag list is
// sorted by name within each entry, so regenerating the manifest from
// an unchanged registry produces a byte-identical file.

package commands

import (
	"flag"
	"sort"

	"github.com/dereksantos/cortex/pkg/cliout"
)

// GenerateManifest walks the registry and returns a deterministic
// manifest. fallbackVersion is stamped onto entries whose command
// doesn't implement Versioner (typically the binary's `version`
// constant from main.go).
func GenerateManifest(fallbackVersion string) cliout.ToolManifest {
	names := List() // already sorted
	entries := make([]cliout.ToolEntry, 0, len(names))

	for _, name := range names {
		cmd := Get(name)
		if cmd == nil {
			continue
		}
		entry := cliout.ToolEntry{
			Name:        cmd.Name(),
			Description: cmd.Description(),
			Version:     fallbackVersion,
		}
		if v, ok := cmd.(Versioner); ok {
			if surface := v.SurfaceVersion(); surface != "" {
				entry.Version = surface
			}
		}
		if d, ok := cmd.(ArgsDescriber); ok {
			entry.Args = d.DescribeArgs()
		}
		if d, ok := cmd.(FlagDescriber); ok {
			entry.Flags = collectFlags(d)
		}
		entries = append(entries, entry)
	}

	return cliout.ToolManifest{
		SchemaVersion: cliout.ManifestVersion,
		Generated:     "cortex command registry",
		Tools:         entries,
	}
}

// collectFlags runs the describer against a fresh FlagSet, reads each
// flag back out, and returns the spec list sorted by name. ContinueOnError
// keeps a malformed describer (one that calls fs.Parse) from killing
// the generator — though no current describer does that.
func collectFlags(d FlagDescriber) []cliout.FlagSpec {
	fs := flag.NewFlagSet("manifest", flag.ContinueOnError)
	d.DescribeFlags(fs)

	var specs []cliout.FlagSpec
	fs.VisitAll(func(f *flag.Flag) {
		specs = append(specs, cliout.FlagSpec{
			Name:        f.Name,
			Type:        flagTypeOf(f),
			Default:     f.DefValue,
			Description: f.Usage,
		})
	})
	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })
	return specs
}

// flagTypeOf inspects the flag's Value with a type switch on the
// concrete pointer types the stdlib flag package emits. Returns
// "string" for unknown types so a future custom flag.Value still
// produces a valid spec (the consumer can fall back to string parsing).
func flagTypeOf(f *flag.Flag) string {
	if g, ok := f.Value.(flag.Getter); ok {
		switch g.Get().(type) {
		case bool:
			return "bool"
		case int:
			return "int"
		case int64:
			return "int64"
		case uint:
			return "uint"
		case uint64:
			return "uint64"
		case float64:
			return "float64"
		case string:
			return "string"
		}
	}
	return "string"
}
