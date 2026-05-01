# Paper Reference Updates TODO

## Status: In Progress
**Last Updated**: 2026-01-07

---

## Completed

- [x] Add Time-Scaling reference [3] to OnContextEvolution.md
- [x] Add Kahneman dual-process theory [2] to Section 2
- [x] Remove Clean Code/Refactoring/DDD references
- [x] Add RAG reference [4] with proper citation
- [x] Add DeepSeek-R1 reference [5] with discussion
- [x] Update Section 8 (Relationship to Existing Work)

---

## High Priority

### 1. Add Time-Scaling Reference
**File**: `OnContextEvolution.md`, `ABSTRACT.md`
**Action**: Add citation and discussion

```markdown
[X] Liu, Zhi and Guangzhi Wang. "Time-Scaling Is What Agents Need Now."
    arXiv:2601.02714, January 2026.
```

**Where to add in OnContextEvolution.md Section 8:**
> **Time-Scaling** [X] proposes extending an agent's reasoning over time as essential for problem-solving without increasing model parameters. Context evolution implements Time-Scaling through activity-based budgets: Think processes during active periods, Dream explores during idle periods, and session context accumulates over interactions. Where Time-Scaling describes the conceptual framework, Cortex provides a concrete implementation with measurable ABR metrics.

---

### 2. Add Dual-Process Theory (Cognitive Science Grounding)
**File**: `OnContextEvolution.md`
**Action**: Add citation to justify Reflex/Reflect terminology

```markdown
[X] Kahneman, Daniel. "Thinking, Fast and Slow." Farrar, Straus and Giroux, 2011.
```

**Suggested addition to Section 2 (Mechanical and Agentic Separation):**
> This separation mirrors dual-process theory in cognitive science [X]: System 1 (fast, automatic, intuitive) corresponds to Reflex; System 2 (slow, deliberate, analytical) corresponds to Reflect. The terminology is deliberate—developers interact with AI as if it were a human-like collaborator, and matching the cognitive metaphor improves intuition about system behavior.

---

### 3. Remove Clean Code/Refactoring/DDD from OnContextEvolution.md
**File**: `OnContextEvolution.md`
**Action**: Remove references [2], [3], [4] - they're tangential to temporal learning

**Current (to remove):**
```markdown
[2] Martin, Robert C. Clean Code: A Handbook of Agile Software Craftsmanship. Prentice Hall, 2008.
[3] Fowler, Martin. Refactoring: Improving the Design of Existing Code. Addison-Wesley, 2018.
[4] Evans, Eric. Domain-Driven Design: Tackling Complexity in the Heart of Software. Addison-Wesley, 2003.
```

**Note**: Keep these in ABSTRACT.md where they're used as quality standards for eval metrics.

---

### 4. Add RAG Foundation Reference
**File**: `OnContextEvolution.md`, `ABSTRACT.md`
**Action**: Add foundational RAG citation

```markdown
[X] Lewis, Patrick et al. "Retrieval-Augmented Generation for Knowledge-Intensive
    NLP Tasks." NeurIPS 2020.
```

**Suggested addition to Section 8 (Relationship to Existing Work):**
> **Retrieval-Augmented Generation (RAG)** [X] retrieves context to augment LLM generation. Context evolution extends RAG with *temporal learning*—retrieval quality improves over time through background processing, not just static retrieval.

---

### 5. Add DeepSeek-R1 Reference
**File**: `OnContextEvolution.md`
**Action**: Add citation and brief mention

```markdown
[X] DeepSeek AI. "DeepSeek-R1: Incentivizing Reasoning Capability in LLMs via
    Reinforcement Learning." Technical Report, January 2025.
```

**Suggested addition to Section 8:**
> **Explicit Reasoning Trajectories.** Recent models like DeepSeek-R1 [X] use reinforcement learning to develop explicit reasoning traces. Context evolution complements this: where R1 improves *within-turn* reasoning, Cortex improves *across-turn* context accumulation. The Reflect mode could leverage such reasoning-capable models for higher-quality reranking.

---

## Medium Priority

### 6. Add MemGPT Reference
**File**: `ABSTRACT.md`
**Action**: Add to Related Work section

```markdown
[X] Packer, Charles et al. "MemGPT: Towards LLMs as Operating Systems."
    arXiv:2310.08560, October 2023.
```

**Note**: MemGPT addresses memory management for LLMs through a hierarchical memory system. Cortex differs by focusing on developer-specific context and latency constraints.

---

### 7. Consider Adding Real-Time Systems Literature
**File**: `OnContextEvolution.md`
**Action**: Optional - strengthen the <20ms latency constraint justification

Possible citations:
- Nielsen, Jakob. "Response Times: The 3 Important Limits" (50ms imperceptibility threshold)
- Real-time systems literature on latency budgets

---

## Reference Renumbering

After changes, OnContextEvolution.md references should be:

| New # | Citation |
|-------|----------|
| [1] | ACE (Agentic Context Engineering) |
| [2] | Time-Scaling (Liu & Wang, 2026) |
| [3] | Kahneman - Thinking, Fast and Slow |
| [4] | RAG (Lewis et al., 2020) |
| [5] | DeepSeek-R1 |
| [A] | Cortex (reference implementation) |

---

## Checklist

- [x] Update OnContextEvolution.md Section 8 with new references
- [x] Add Kahneman to Section 2 (cognitive science grounding)
- [x] Remove Clean Code/Refactoring/DDD from OnContextEvolution.md
- [ ] Update ABSTRACT.md Related Work with Time-Scaling
- [x] Renumber all references in OnContextEvolution.md
- [ ] Verify all arXiv links are correct
- [ ] Run spell check on updated sections
