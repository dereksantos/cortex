# Developer Experience

## UX
A classic coding loop with think, read, edit, bash, study and journalling.

```
[time] [message] (color coded by role)

❯ The users prompt.
·Cortex 0.1.0· [mode] · [context]
```

Syntax highlighting
Inline diffs
Markdown rendering

Agent Tools: `think` `study` `journal` `dream` `read_file` `edit_file` `bash` 

### Full Autonomy Mode (Experimental)

In this mode verifiable outcomes are the best prompts. If the agent can accurately verify its work, it can iterate, work through bugs, and adapt until the outcomes are verified.

Every turn in a conversation is a fresh context window so that it can continue for much longer periods of time. Execution patterns emerge based on classified intent.

### Setup Experience
Cortex needs either an OpenRouter key or local models to work. Setting up local models can be complex work, so how do we streamline the setup experience? Could we run basic evals to assess your model choices (though we'd need a model so its chicken and egg). Best is probably write an install prompt you can give your existing harness and model of choice.

### Safety

Security classifier auto runs on bash, web and edits.
Consider other improvements

### Thinking

The Think patter in cortex should be applied to this harness so ABR can be measured and the approach applied in the harness.

### Studying

Studying uses a subagent with an optionally dedicated model for reading and understanding project files and data. It prevents contex pollution through by sampling the content dynamically.

## Journalling

Cortex automatically writes to journals and asynchronously and agentically classifies them into memory.

## Dreaming

Dreaming occurs when the models are idle from active sessions. Project contents are randomly selected and run through a model to produce insights, ideas, possibilities, and re-inforce learned patterns.

