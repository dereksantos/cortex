## Cortext Pre-release Version

### Loop and study refactoring

Cleaning up the harness so its tigher and better architecturally. 
Functional and powerful with the basic harness tools + study + memory

#### Functional Requirements:

*REPL*
no TUI, doesnt take over terminal screen
Every action in the harness is visible in the REPL
Keep output brief and simple
Review this part and make it as good and simple as possible

*Tools*

Coding and general purpose

```
read_file
edit_file
write_file
bash
```

*Memory Tools*

Journal

Can we reliably make it generate these categories with a classifier? How can journal enable an adaptive harness that continouly learns.
Notes
Memories
Directives

*Study Tools
outline
read_file
grep
classify

## Cortext 1.0 Release

### Response pre flight
Develop a template for the response that is easy to read and digest, and respond that way. Dont make simple things sound complicated. Layers of tiering and machinery and can often be too much

### Loop engineering

### Harness Personality

It should favour doing the right architectural thing, not hacks or fast and cheap to achieve the goal, unless explicitly asked to do it that way. It should favour tidy first, green/red tdd, green/red eval for agentic flows, etc.

### Hooks and extensability

### Skills

### Think, Dream

### Proactivity
- proactive studying, thinking and dreaming, visible in the tool.

## Session working memory

 
## Web interface (Major Version 2)

Managing context across sessions, projects, and agentic tools.

## Agentic Load

What's the context difficulty and scope the agent is dealing with. Perhaps apply a classifier.

## Integrated Secrets Manager

## Editor integration via ACP

Make Cortex speak ACP (Agent Client Protocol) so Cortex drops into existing
Neovim front-ends (agentic.nvim, CodeCompanion) as a back-end — no bespoke
`cortex.nvim` plugin to write or maintain. Work lives in a Go ACP adapter over
the existing `Session.Turn` / `cortex turn` seam, not in Lua.

The differentiator: every Neovim AI plugin today assumes a frontier model
behind it. Cortex's thesis — small/local models + working memory + journal — is
a genuinely different back-end behind the same front-end. Not "another Claude
wrapper for the editor"; "what the editor feels like when the agent has
persistent memory and runs locally."

Open question before committing: does ACP map cleanly onto `Session.Turn`, and
is there a usable Go ACP library?


