# The Learning Harness

**Draft v0.2** — *Cortex in the agent memory and harness landscape*

## Abstract

This paper describes Cortex, a system for **continuous context integration** in service of agents executing tasks. We use *learning harness* as descriptive language for the role this layer plays alongside coding harnesses (Claude Code, Cursor, Aider, Claude Agent SDK [1]) and agent memory systems (Mem0 [2], Letta [3], A-Mem [4], Claude Code Auto-Memory, AWS AgentCore [5]). The framework is grounded in CoALA [6], which formalized cognitive architectures for language agents in 2023; Cortex is one specific instantiation. We engage directly with the cluster of recent work that overlaps most closely — sleep-time compute [7], BudgetMem [8], the reflection loop in Generative Agents [9], skill accumulation in Voyager [10], and memory evolution in A-Mem [4] — and describe two specific architectural commitments in Cortex: (a) an *inverse activity gradient* on background processing, where Think runs at reduced budget during active periods and Dream grows during idle, refining the binary active/idle split common in sleep-time approaches; and (b) a *mechanical foreground with a latency target*, where retrieval on the critical path targets <20ms with agentic processing happening off-path. We position the small-model salience layer as the application being built to test whether continuous integration measurably helps small local models produce well-architected code at a fraction of the cost.

## 1. What is a learning harness

Modern AI agents in production rely on two well-established classes of infrastructure:

- **Coding harnesses** [1] execute the agent's task loop: receive intent, plan, call tools, return responses. Turn-bounded, synchronous, episodic.
- **Memory systems** [2, 3, 4, 5] persist information across sessions and surface it on demand via retrieval.

A *learning harness* is descriptive language for a third role: a layer that runs continuously alongside these, observing events as they happen, reconciling new information against existing knowledge, and surfacing relevant context proactively rather than only on demand. This role is not a new category — it sits within the framework of cognitive architectures for language agents [6], and is closely related to the sleep-time / background-agent pattern [7] and to memory-evolution approaches [4].

We use the term because it is useful to name the role concretely. The defining behavior is the loop:

```
observe → think → dream → consolidate → propose
```

A useful intuition: the learning harness is the little voice in the head of an agent — not the part doing the work, and not the filing cabinet of past work, but the reflective process that notices patterns, reconciles new observations with what is already known, and surfaces what is relevant. The remainder of this paper describes how Cortex implements that role and what it adds to the existing literature.

## 2. Lineage and concurrent work

Cortex draws from five overlapping threads.

### 2.1 Cognitive architectures for language agents

CoALA [6] formalized the architectural pattern Cortex follows: an agent with modular memory components (working and long-term), an action space split between internal cognitive actions (retrieval, reasoning, learning) and external environment interactions, and a continual decision loop alternating planning and execution. Cortex's five-mode taxonomy maps onto this framework directly: Reflex and Reflect are retrieval and reasoning actions; Resolve drives the decision step; Think and Dream are learning actions that write to long-term memory.

### 2.2 Memory in agents

Production-scale agent memory systems — Mem0 [2], Letta [3], A-Mem [4], AWS AgentCore [5], Claude Code Auto-Memory, Cursor memories — established the storage and recall pattern: extraction at write time, embedding-based retrieval, importance and recency weighting. A-Mem [4] in particular implements *memory evolution*, where new memories trigger updates to the contextual representations of existing memories, allowing the network to continuously refine its understanding. This is the closest published precedent to what we mean by continuous context integration. The recent survey "Memory in the Age of AI Agents" [11] catalogs the broader space.

### 2.3 Reflection in long-running agents

Generative Agents [9] introduced a reflection loop: NPCs in a simulated town periodically synthesized higher-level insights from their memory streams. Voyager [10] extended the pattern to skill accumulation, with an agent in Minecraft maintaining a library of verified code skills and proposing its own next challenge using what it has already learned. Both established that a separate reflective process — distinct from the action loop — produces compounding gains. Cortex inherits this design directly.

### 2.4 Sleep-time compute and budget-aware memory

Sleep-time compute [7], from Letta and UC Berkeley, is the closest direct match for Cortex's Dream mode. Sleep-time agents run during idle periods between user interactions, asynchronously modifying memory blocks while the primary agent waits for input. Reported results: roughly 5× reduction in live token budgets and ~15% accuracy improvement. The Letta production system implements this as a separate sleep-time agent paired with each primary agent.

BudgetMem [8] introduces query-aware budget-tier routing for runtime agent memory, with explicit Low/Mid/High budget tiers along implementation, reasoning, and capacity axes. It demonstrates that bounded, configurable memory operations are tractable and useful.

### 2.5 Context engineering

The discipline that crystallized in 2025-2026 [12] reframed the bottleneck in agent systems from raw model capability to context quality. Agentic Context Engineering [12] in particular formalized contexts as evolving playbooks updated through Generation, Reflection, and Curation. Cortex adopts the same framing.

## 3. Cortex

Cortex is a single-binary daemon in Go. It captures events from coding harnesses (Claude Code, Cursor, Aider, or any MCP-enabled client) via lightweight hooks, stores them in SQLite with vector embeddings, and runs five cognitive modes:

| Mode | Type | Purpose |
|------|------|---------|
| Reflex | Mechanical | Retrieval on the critical path, <20ms target |
| Reflect | Agentic | Reranking and contradiction detection |
| Resolve | Agentic | Decide whether to inject, wait, or queue |
| Think | Agentic, background | Active-period consolidation |
| Dream | Agentic, background | Idle-period exploration |

The mode taxonomy follows the CoALA pattern [6]. Two specific architectural commitments distinguish Cortex from the closest concurrent systems.

### 3.1 Inverse activity gradient on background processing

Sleep-time compute [7] runs background agents during idle periods. The split is effectively binary: active work happens in the primary agent; reflection happens in the sleep-time agent during idle windows. BudgetMem [8] tunes memory operation cost per query.

Cortex extends this with a continuous gradient tied to host activity:

```
ThinkBudget  = MaxBudget × (1 - ActivityLevel)        # active periods
DreamBudget  = min(IdleTime × GrowthRate, MaxBudget)  # idle periods
```

Think runs during active periods at reduced budget, doing only what spare cycles allow; Dream runs during idle periods with budget that grows with idle duration up to a cap. The motivation is human: thinking while working depletes capacity; dreaming happens during rest. This is a refinement of the binary active/idle split, not a new paradigm — but it gives the harness a smooth response to host activity rather than a step function.

### 3.2 Mechanical foreground with a latency target

Most agent memory systems are LLM-mediated end-to-end. Cortex's design splits the foreground retrieval path away from any LLM call: Reflex performs embedding similarity, tag matching, and recency weighting, with the agentic modes (Reflect, Resolve, Think, Dream) running off-path and feeding Reflex via cached artifacts. The architectural goal is a foreground path with low and predictable latency — <20ms is the target — so quality can compound in the background. This is similar in spirit to RAG-with-rerank patterns and commits explicitly to keeping LLM calls out of the critical path. The latency target is an active engineering goal, not a number Cortex has reliably pinned in real-world use yet; current sessions still see 80–100ms hot-path warnings.

### 3.3 What Think and Dream produce

Think and Dream emit structured artifacts that mechanical retrieval consumes synchronously:

- **Topic weights** — boost scores for results matching session patterns
- **Cached reflections** — pre-computed reranking, so Fast retrieval can approach Full retrieval quality without blocking on an LLM call
- **Resolved contradictions** — conflicts already adjudicated, so Resolve does not re-litigate them
- **Proactive queue** — items Dream considers important enough to surface unprompted

This is the closed loop: agentic processing in the background, mechanical retrieval in the foreground.

## 4. The application being built

A learning harness is worth the engineering only if it produces measurable benefit in a setting that matters. Cortex's primary application is small models writing well-architected code.

**Small models writing well-architected code.** Frontier models compensate for thin context with strong priors; smaller local models rely more directly on the context they receive. If continuous context integration can give a 7B-class model the salience layer it needs — the right decisions, the right patterns, the right contradictions, the right entities, at the right moment — to produce code meeting professional architectural standards, the engineering is justified.

The application is being built on top of Aider [13], with a three-tier knowledge model: surface conventions, architectural patterns, and AST-derived structural facts. The third tier is the leverage point and the hardest to extract — it is exactly the kind of bounded, parallelizable, idle-time work that Dream is designed for.

The Agentic Benefit Ratio (ABR) metric [14] quantifies the gain:

```
ABR = quality(Fast + Think) / quality(Full)
```

ABR approaching 1.0 means the learning loop has internalized enough of the project's structure that fast mechanical retrieval matches the synchronous agentic pipeline. Initial results from a 1.5B-parameter model show an average ABR of 0.77 with session convergence to 1.0 by the second query [14].

### 4.1 Falsification

If small models with full Cortex context still produce code measurably worse than the same models without it, or if continuous integration fails to compound (ABR plateaus below 1.0 across sessions), the application thesis is wrong, not unproven. The engine is being built so the question can be asked properly.

## 5. Open questions

- **Activity detection.** Current heuristics use query rate and tool-call timing. These may not generalize across developer workflows; better signals likely require deeper coordination with the host harness.
- **Cross-harness generalization.** Cortex captures from any MCP-enabled client, but the salience layer is shaped by what each harness exposes.
- **Falsification thresholds.** What is the smallest model class for which continuous context integration produces meaningful benefit? Is there a floor below which no amount of integration helps?
- **Budget tuning.** The inverse-budget model has knobs (MaxBudget, GrowthRate, ActivityWindow) currently set heuristically. Whether these are best learned per-session, per-developer, or per-project is open.
- **Comparison to sleep-time compute.** Whether the inverse activity gradient produces measurable gains over a binary active/idle split, in this application setting, is an empirical question we have not yet tested directly against [7].

## 6. References

[1] *Effective harnesses for long-running agents.* Anthropic Engineering Blog, 2026. anthropic.com/engineering/effective-harnesses-for-long-running-agents

[2] Mem0. github.com/mem0ai/mem0. Also: *Mem0: Building Production-Ready AI Agents with Scalable Long-Term Memory.* arXiv:2504.19413, 2025.

[3] Letta (formerly MemGPT). letta.ai

[4] Xu et al. *A-MEM: Agentic Memory for LLM Agents.* arXiv:2502.12110, 2025.

[5] *Building smarter AI agents: AgentCore long-term memory deep dive.* AWS Machine Learning Blog, 2025.

[6] Sumers et al. *Cognitive Architectures for Language Agents.* arXiv:2309.02427, NeurIPS 2023.

[7] Lin et al. *Sleep-time Compute: Beyond Inference Scaling at Test-time.* arXiv:2504.13171, 2025. Production implementation: Letta sleep-time agents.

[8] Axelsen. *BudgetMem: Learning Query-Aware Budget-Tier Routing for Runtime Agent Memory.* github.com/ViktorAxelsen/BudgetMem

[9] Park et al. *Generative Agents: Interactive Simulacra of Human Behavior.* UIST 2023.

[10] Wang et al. *Voyager: An Open-Ended Embodied Agent with Large Language Models.* NVIDIA, 2023.

[11] *Memory in the Age of AI Agents: A Survey.* arXiv:2512.13564, 2025.

[12] *Agentic Context Engineering.* arXiv:2510.04618, 2024.

[13] Aider. aider.chat

[14] *Cortex: A Reference Implementation of Context Evolution.* See [abstract.md](abstract.md).

---

*For implementation details see [product.md](product.md). For evaluation methodology see [eval.md](eval.md). For theoretical foundations see [context-evolution.md](context-evolution.md).*
