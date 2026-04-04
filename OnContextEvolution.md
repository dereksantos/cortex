# On Context Evolution

**Temporal Learning for AI-Assisted Software Development**

*Draft v0.1 — December 2024*

---

## Abstract

Large language models operate statelessly—each interaction begins fresh, forgetting decisions, corrections, and patterns established moments before. We propose *context evolution* as a paradigm for persistent, temporal learning in AI-assisted development. Context evolution separates mechanical retrieval (fast, reflexive) from agentic processing (slow, deliberate), using activity-based budgets that mirror human cognition. We introduce the *Agentic Benefit Ratio* (ABR) to measure whether background processing improves retrieval quality over time. The framework applies to any AI assistant operating over temporal context sets—conversations, project files, tool usage—and suggests that real-world developer productivity can be increased through principled context engineering.

---

## 1. Evolving Temporal Performance of LLMs

AI coding assistants have made significant progress on session persistence—Claude Code's Auto-Memory captures preferences and corrections natively, and "context engineering" has emerged as a recognized discipline. Yet these approaches share a limitation: they treat context as flat text to be stuffed into prompts, without principled resource management or quality measurement.

The gap is not *whether* to persist context, but *how* to manage it at scale. As projects grow, flat-file memory becomes noise. Background processing runs unbounded. There is no metric for whether injected context actually improves output quality. Software development is inherently temporal—decisions compound, patterns emerge, and context evolves—and effective memory systems must model this temporality with bounded, measurable processing.

Building on prior work in agentic context engineering [1] and time-scaling [3], context evolution proposes principled separation of mechanical and agentic processing, activity-based budgets, and measurable quality via ABR.

### 1.1 What Context Evolution Means

To push the capabilities of LLMs, a multi-stage process must execute as a *reflex* to user input. Reflexes then spawn mechanical and agentic processes alike, with the goal of evolving what is mechanical into more relevant context—optimizing the speed of context engineering and its accuracy.

The key insight: **on-demand use of local resources can improve AI effectiveness.** But "on-demand" requires understanding *when* to act and *how much* capacity to allocate.

---

## 2. Mechanical and Agentic Separation

Context evolution rests on a fundamental separation:

| Type | Speed | Intelligence | Example |
|------|-------|--------------|---------|
| **Mechanical** | <20ms | None | Keyword matching, recency weighting |
| **Agentic** | 200ms+ | LLM-based | Reranking, contradiction detection |

This separation mirrors dual-process theory in cognitive science [2]: System 1 (fast, automatic, intuitive) corresponds to mechanical processing; System 2 (slow, deliberate, analytical) corresponds to agentic processing. The terminology is deliberate—developers interact with AI as if it were a human-like collaborator, and matching the cognitive metaphor improves intuition about system behavior.

**Why 20ms?** Latency under 50ms is imperceptible to humans. Targeting 20ms provides headroom—multiple hooks can run without crossing the perceptual threshold. The goal is "feels instant," not a specific number.

As other agentic processes may also depend on LLMs, so reducing response time is crucial for agentic processes.

Mechanical processes provide the speed necessary for interactive use. Agentic processes provide the intelligence necessary for quality. Neither alone is sufficient.

### 2.1 The Retrieval Path

A minimal context evolution system requires three stages:

1. **Reflex** — Mechanical retrieval that *feels* instantaneous. Hard latency budget. Returns partial results rather than blocking.

2. **Reflection** — Agentic reranking that asks "is this actually relevant?" Runs synchronously when accuracy matters, asynchronously when speed matters.

3. **Resolution** — Agentic decision-making that asks "should I surface this now, or wait?" Models human judgment about when to speak and when to hold back.

### 2.2 Background Processing

Beyond the retrieval path, context evolution benefits from *background* processing:

- **Think** — Active work generates context. Background processes can learn patterns, pre-compute reflections, and resolve contradictions before they're needed.

- **Dream** — Idle time enables exploration. When the developer is away, the system can sample project files, analyze history, and discover patterns not yet queried.

The key is *budgeting*: background processing must not compete with foreground work.

---

## 3. Activity-Based Budget Models

Unlike "run in background" approaches that treat capacity as binary, context evolution models cognitive load:

### 3.1 Think Budget (Active Periods)

```
ThinkBudget = MaxBudget × (1 - ActivityLevel)
```

When the developer is busy (rapid queries, many tool calls), Think does less. When there are pauses, Think does more. This mirrors human cognition: thinking while working exhausts cognitive resources.

### 3.2 Dream Budget (Idle Periods)

```
DreamBudget = min(IdleTime × GrowthRate, MaxBudget)
```

When the developer is idle, Dream explores. Longer breaks enable deeper processing. Like human dreaming, it performs exploration when resting—not when active.

### 3.3 The Cognitive Metaphor

This terminology is deliberate. We borrow from human cognition not because machines are human, but because developers *interact* with machines as if they were human-like collaborators. Matching the metaphor improves intuition about system behavior.

---

## 4. Agentic Benefit Ratio (ABR)

How do we measure whether agentic processing is *worth* the compute?

We introduce ABR:

```
ABR = quality(Fast + Background) / quality(Full)
```

Where:
- **Fast + Background** = mechanical retrieval plus whatever background processing has accumulated
- **Full** = synchronous agentic pipeline (slower, but more accurate)

### 4.1 Interpretation

- **ABR < 0.8**: Background processing is not learning effectively
- **ABR ≈ 0.9**: Good—Fast is nearly as good as Full
- **ABR → 1.0**: Optimal—background processing fully compensates for skipping synchronous reflection

### 4.2 Temporal Hypothesis

ABR should *increase over a session* as background processing accumulates context. Early queries may show low ABR; subsequent queries should approach 1.0.

This is measurable. This is the core claim of context evolution: **mechanical retrieval can approach agentic quality through temporal learning.**

---

## 5. Evaluation Through the SDLC

Context evolution measurements can be made from temporal context sets: past conversations, project files, tool usage. The Software Development Lifecycle (SDLC) provides a natural evaluation context.

### 5.1 A Temporal Scenario

Let `s` represent a project subject to multi-session AI tool usage, with context becoming available temporally:

| Phase | Developer Action | Context Evolution |
|-------|------------------|-------------------|
| **Build** | "Build a prototype of `s`" | System learns project structure |
| **Mature** | "Make `s` production-ready" | System suggests past decisions, patterns |
| **Debug** | "Fix critical bug in `s`" | System surfaces relevant constraints |
| **Retro** | "Review the project" | System provides historical context |
| **Scale** | "Add scalability improvement" | System tracks evolving patterns |
| **Migrate** | "Major architecture change" | System increases use of new patterns |
| **Observe** | "Project at scale" | System adapts to larger context |

### 5.2 The Research Question

If context evolution could be applied to the software development lifecycle, could real-world productivity be increased?

The context evolution eval ought to consider the entire SDLC as its test bed, asserting that agentic reflection of context on demand can guide higher-quality AI-augmented software development.

---

## 6. Tunable Parameters

Context evolution is not one-size-fits-all. Key parameters:

| Parameter | What It Controls | Trade-off |
|-----------|------------------|-----------|
| **Hint Size** | How much context to inject | Too much → context pollution; too little → missed information |
| **Decay** | How quickly Think/Dream budgets decay | Fast decay → more responsive; slow decay → deeper processing |
| **Depth** | Budgets for Reflect, Think, Dream | Higher → better quality; lower → faster, cheaper |

These parameters define the *behavior* of context evolution, separate from any specific implementation. Additional parameters may emerge from practice—embedding model selection, similarity thresholds, and proactive injection confidence levels are implementation-specific tuning knobs that affect the above trade-offs.

---

## 7. Agentic Random Reflection

A speculative extension: could Dream utilize *fractal traversal patterns* for randomly learning about a codebase more effectively?

The idea:
1. **Mechanical fractal retrieval** — Sample project structure at multiple scales (file, function, line)
2. **Agentic analysis** — Prompt for idea generation, suggest improvements
3. **Pattern discovery** — Surface insights not yet queried

This "agentic random reflection" could improve project outcomes by exploring what the developer hasn't thought to ask about—yet. This is implemented in Cortex as the Dream mode with pluggable DreamSource interfaces (Project, Cortex, Claude History, Git sources).

---

## 8. Relationship to Existing Work

Context evolution builds on several threads:

**Agentic Context Engineering (ACE)** [1] treats contexts as "evolving playbooks" with Generation, Reflection, and Curation operations. Context evolution adds explicit *latency constraints* and *activity-based budgets*.

**Time-Scaling** [3] proposes extending an agent's reasoning over time as essential for problem-solving without increasing model parameters. Context evolution implements Time-Scaling through activity-based budgets: Think processes during active periods, Dream explores during idle periods, and session context accumulates over interactions. Where Time-Scaling describes the conceptual framework, Cortex provides a concrete implementation with measurable ABR metrics.

**Native AI Memory.** Claude Code (since v2.1.59) ships Auto-Memory and Auto-Dream—automatic preference capture and between-session consolidation modeled after REM sleep. These handle ~70-80% of basic recall for solo developers but lack semantic retrieval, cross-tool portability, budget-bounded processing, and quality measurement. Context evolution provides the principled architecture that native memory approximates.

**Observational Memory.** Mastra's observational memory framework captures context from tool interactions and surfaces it proactively. Context evolution adds cognitive mode separation and activity-based budgets to this observational approach.

**Context Engineering as Discipline.** The term "context engineering" emerged in 2025-2026, recognizing that the bottleneck has shifted from model intelligence to context quality. Context evolution is a specific proposal within this discipline, contributing budget-bounded processing and ABR as a quality metric.

**LLM Functional Anatomies.** Research on layer duplication in LLMs [6] demonstrates distinct cognitive circuits for different reasoning tasks—different layer ranges activate for math vs. emotional processing. This validates the cognitive metaphor underlying context evolution: different modes (Reflex, Reflect, Dream) for fundamentally different processing needs.

**Retrieval-Augmented Generation (RAG)** [4] retrieves context to augment generation. Context evolution extends RAG with *temporal learning*—retrieval quality improves over time through background processing, not just static retrieval.

**Explicit Reasoning Trajectories.** Recent models like DeepSeek-R1 [5] use reinforcement learning to develop explicit reasoning traces. Context evolution complements this: where R1 improves *within-turn* reasoning, Cortex improves *across-turn* context accumulation. The Reflect mode could leverage such reasoning-capable models for higher-quality reranking.

**Memory-Augmented Language Models** persist state across interactions. Context evolution adds *cognitive load modeling*—background processing yields to foreground work.

---

## 9. Implications

If context evolution is viable:

1. **AI assistants become more effective over time** — not through fine-tuning, but through accumulated context

2. **Developer corrections are permanent** — "We use X, not Y" persists across sessions

3. **Architectural decisions are remembered** — patterns and constraints are surfaced automatically

4. **Quality can be measured** — ABR provides a quantitative metric for context engineering effectiveness

---

## 10. A Reference Implementation

Cortex [A] implements context evolution as a single-binary CLI daemon. It separates Reflex (mechanical, <20ms target) from Reflect/Resolve (agentic, 200ms+), uses activity-based Think/Dream budgets, and measures ABR across sessions.

Initial evaluations show:
- 87% pass rate across cognitive mode evaluations
- ABR of 0.77 (Fast mode achieves 77% of Full mode quality)
- Session convergence to ABR ≈ 1.0 by the second query

These results suggest context evolution is not merely theoretical—it is implementable and measurable.

---

## 11. Conclusion

Context evolution proposes that AI assistants can learn over time through principled separation of mechanical and agentic processing, activity-based budgets that mirror human cognition, and temporal accumulation of context. The Agentic Benefit Ratio provides a metric for measuring whether this learning is effective.

The broader question: can real-world developer productivity be increased through context evolution? The SDLC provides a test bed. The framework provides a structure. The measurement provides accountability.

What remains is to build, evaluate, and iterate.

---

## References

[1] Agentic Context Engineering. arXiv:2510.04618, 2024.

[2] Kahneman, Daniel. Thinking, Fast and Slow. Farrar, Straus and Giroux, 2011.

[3] Liu, Zhi and Guangzhi Wang. "Time-Scaling Is What Agents Need Now." arXiv:2601.02714, January 2026.

[4] Lewis, Patrick et al. "Retrieval-Augmented Generation for Knowledge-Intensive NLP Tasks." NeurIPS 2020.

[5] DeepSeek AI. "DeepSeek-R1: Incentivizing Reasoning Capability in LLMs via Reinforcement Learning." Technical Report, January 2025.

[6] Heekerens, Daniel. "Layer Duplication in LLMs: Functional Anatomies of Large Language Models." dnhkng.github.io/posts/rys/, 2025.

[A] Cortex: Latency-Constrained Cognitive Architecture for Developer Context Memory. Reference implementation. github.com/[repo], 2024.

---

*This paper presents context evolution as a paradigm. Cortex is one implementation. The concepts—mechanical-agentic separation, activity-based budgets, ABR, temporal learning—are applicable beyond any single tool.*
