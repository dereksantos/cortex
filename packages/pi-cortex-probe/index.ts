import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { Type } from "typebox";

export default function (pi: ExtensionAPI) {
  pi.registerTool({
    name: "pi_cortex_probe",
    label: "Cortex Probe",
    description:
      "Hello-world probe used to verify the cortex extension wiring against pi.dev's auto-discovery and tool-call event stream. Returns a constant string. Delete after Phase 8 TODO 2.",
    parameters: Type.Object({}),
    execute: async () => ({
      content: [{ type: "text", text: "hello from cortex probe" }],
      details: {},
    }),
  });
}
