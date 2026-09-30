/**
 * Output-schema shells.
 *
 * Every tool declares an outputSchema so a caller knows the result shape before
 * spending a probe call.
 *
 * The failure path must be modelled here, not omitted. The server skips
 * validation when `isError` is set, but the CLIENT does not: it checks every
 * structuredContent against the declared schema, and the generated JSON Schema
 * carries `additionalProperties: false`. An unmodelled error envelope therefore
 * surfaces as a protocol error instead of a readable message — strictly worse
 * than declaring no schema at all. Verified against SDK 1.29 with a live stdio
 * client.
 *
 * Consequence: `data` and `metadata` are optional, and `error` is a peer. The
 * shape still tells a caller what a SUCCESSFUL call returns, which is the point.
 *
 * Two tiers for `data`:
 *   Tier A — a precise loose shape, for chainable reads whose real output has
 *            been captured and parsed. Loose so an added upstream key is never
 *            a breaking change.
 *   Tier B — `z.unknown()`, the default. Still names `data` / `metadata`, which
 *            is what removes the probe call.
 *
 * A precise schema that has drifted from reality is worse than none: it turns a
 * working tool into an McpError on the success path. Tier B is the safe default;
 * Tier A is earned per tool.
 */
import { z } from "zod";

export const truncationSchema = z
  .looseObject({
    fields: z.array(z.string()).describe("Paths of the string leaves that were shortened."),
    original_chars: z.number().describe("Serialized size before truncation."),
    limit: z.number().describe("Per-string character cap applied."),
  })
  .optional()
  .describe("Present only when output was shortened to fit the caller's context budget.");

export const errorSchema = z
  .looseObject({
    message: z.string(),
    why: z.string().nullish().describe("What actually went wrong underneath."),
    hint: z.string().nullish().describe("The next action to take."),
  })
  .optional()
  .describe("Present instead of data when the call failed.");

/** Shell for scripts that emit the JSON envelope. */
export function envelopeOutput(data: z.ZodTypeAny = z.unknown()): z.ZodRawShape {
  return {
    data: data.optional().describe("Present on success."),
    metadata: z.record(z.string(), z.unknown()).optional().describe("Script-supplied context about the result."),
    error: errorSchema,
    _truncation: truncationSchema,
  };
}

/** Shell for bash/Swift atoms that emit raw streams. */
export function rawOutput(): z.ZodRawShape {
  return {
    stdout: z.string(),
    stderr: z.string(),
    exit_code: z.number(),
    timed_out: z.boolean().optional().describe("Set when the script was killed at the timeout."),
    message: z.string().optional().describe("Present alongside timed_out."),
    _truncation: truncationSchema,
  };
}
