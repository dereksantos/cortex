import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

/**
 * Cortex extension factory for pi.dev.
 *
 * Phase 8 TODO 2 — scaffold stub. Loads cleanly, registers nothing.
 *
 * Subsequent ticks fill this in:
 *   TODO 3-6: register `cortex_recall` (Reflex entrypoint) wired to
 *             `cortex search --format json`.
 *   TODO 7:   hook `tool_call` to forward redacted events to
 *             `cortex capture --type pi_tool_call`.
 */
export default function cortexExtension(pi: ExtensionAPI): void {
  void pi;
}
