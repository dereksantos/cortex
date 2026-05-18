// Package ops implements the registered DAG node handlers per
// docs/dag-build-plan.md Stage 2.
//
// Each op is a `<function>.<op>` named handler conforming to
// dag.Handler. Two flavors:
//
//   - Mechanical ops (represent.embed, remember.vector_search) wrap an
//     embedder or storage call directly. No LLM. Returned cost reports
//     measured wall time; tokens = 0.
//
//   - LLM ops (attend.rerank, value.score, value.detect_contradiction,
//     decide.inject, decide.should_capture, model.predict_next,
//     maintain.extract_insight) load a versioned prompt template from
//     pkg/cognition/prompts/, invoke an llm.Provider, parse the
//     structured response, and fall back to a mechanical heuristic
//     when budget is too thin (per the budget-aware self-modulation
//     pattern in dag-protocol.md "Handler signature").
//
// Each handler is constructed by a factory `NewXxxHandler(cfg)` that
// captures dependencies (embedder, storage, provider) at registration
// time. Defaults.go composes them into a single Registry suitable for
// `cortex run --type=turn`.
package ops
