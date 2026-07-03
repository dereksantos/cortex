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
