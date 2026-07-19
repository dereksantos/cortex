// Package cache implements the two-zone working-memory cache described in
// docs/context-architecture.md: an append-stable prefix (system + a
// deterministic outline of demoted turns + the memory index) and a
// watermarked hydrated tail of the most recent turns. Older turns demote
// mechanically to outline lines with citations rather than being
// restructured or dropped. This file is the original design sketch that
// grew into that doc; docs/context-architecture.md is the current spec.
package cache

/*

Design: docs/context-architecture.md (the two-zone cache-friendly context
model this sketch grew into; this package becomes the working-set model in P2).

WorkingMemoryCache

User messages are immutable.
Never restructure context. Use determinstic citation in place of raw output, unless its the raw output of the last set of tool calls.


Two types of context:

User context - immutable, always sent
Working context - immutable structure, outlined and searchable


Cortex Context Architecture

Prefix
---------------------
[ ] System prompt
[ ] AGENTS.md
[ ] Session Outline

Turn
---------------------
[ ] Last n items from outline hyrdrated to fit provided budget


*/
