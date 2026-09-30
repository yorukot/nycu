import { describe, expect, test } from "bun:test";
import { z } from "zod";
import { mapScriptOutput } from "../src/core/exec.ts";
import { allTools } from "../src/tools/index.ts";
describe("failure paths satisfy their own output schema", () => {
  const run = (over: Partial<Parameters<typeof mapScriptOutput>[1]> = {}) => ({
    stdout: "",
    stderr: "",
    exitCode: 1,
    timedOut: false,
    timeoutMs: 1000,
    argv0: "example.py",
    ...over,
  });

  const CASES = {
    "script reported failure": run({ stdout: JSON.stringify({ success: false, error: { message: "boom", why: "because", hint: "do x" } }) }),
    "failure with bare message": run({ stdout: JSON.stringify({ success: false, error: { message: "boom" } }) }),
    "failure with no error detail": run({ stdout: JSON.stringify({ success: false }) }),
    "crash before any output": run({ stderr: "Traceback (most recent call last):", exitCode: 2 }),
    "json that is not an envelope": run({ stdout: JSON.stringify({ whatever: true }) }),
    "timeout": run({ timedOut: true, exitCode: -1 }),
  };

  // Every NYCU operation uses the same envelope contract.
  const scriptTools = allTools.filter((tool) => "data" in tool.outputSchema || "stdout" in tool.outputSchema);

  for (const tool of scriptTools) {
    const isEnvelope = "data" in tool.outputSchema;
    const schema = z.object(tool.outputSchema);

    for (const [label, scriptRun] of Object.entries(CASES)) {
      test(`${tool.name}: ${label}`, () => {
        const result = mapScriptOutput(isEnvelope, scriptRun);
        const parsed = schema.safeParse(result.structuredContent);

        expect(parsed.success ? null : { issues: parsed.error.issues, got: result.structuredContent }).toBeNull();
        expect(result.isError).toBe(true);
      });
    }
  }


});
