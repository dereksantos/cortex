package main

import _ "embed"

// defaultConfigSkill is the built-in default Agent Skill — the one Cortex
// ships for writing its own config. It is NOT discovered from any
// filesystem root: skillsIndexNote (skills.go) synthesizes a single index
// line for it (pointing at defaultConfigSkillPath) and
// defaultConfigSkillFor intercepts a read_file of that path to hand back
// the embedded SKILL.md text — the progressive-disclosure design the
// standard calls for, with the body living in the binary instead of on disk.
// A filesystem skill named "writing-config" in any discovery root
// SHADOWS the built-in (discovery dedupes by name: first dir wins) —
// the user's own playbook for the same job replaces this one.
//
// The body is deliberately field-level, not byte-level: every field it
// names is one the Config struct (cmd/cortex/config.go) actually reads, and
// the layered merge rules mirror docs/configuration.md — the verified
// source of truth the model is pointed at for the details. It stays short
// on purpose: this text rides into context verbatim on every read, and the
// same small-local-model attention constraint that caps the base system
// prompt (TestSystemPromptSizeBudget) applies here.

//go:embed default_skills/writing-config/SKILL.md
var defaultConfigSkillText string

const defaultConfigSkillName = "writing-config"

const defaultConfigSkillPath = "<default skills>/writing-config/SKILL.md"

const defaultConfigSkillDescription = "Write or update .cortex/config.json — every config field, its type, and the layered merge rules."
