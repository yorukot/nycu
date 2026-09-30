import { z } from "zod";
import { pushFlag } from "../core/argv.ts";
import { scriptTool, type ToolboxTool } from "../core/tool.ts";

const script = "parttime.py";
/** Rides the nycu portal SSO layer: Keychain + headless Chromium, macOS only. Also requires a campus IP. */
const requires = ["platform:darwin", "binary:uv"];
const envelope = true;
// Cold path is a ~25s Chromium login plus a relay and (for sign-in/out) two
// postbacks and a re-fetch to verify the commit; 60s was too tight.
const timeoutMs = 180000;

const read = { readOnlyHint: true, openWorldHint: true } as const;

export const parttimeTools: ToolboxTool[] = [
  scriptTool({
    name: "parttime_get_status",
    description: "Show today's NYCU part-time attendance grid: one row per project period, with sign-in/out availability and accumulated hours. Only reachable from a campus IP (140.113.x) or the lab tailnet.",
    inputSchema: {},
    annotations: read,
    script,
    requires,
    envelope,
    timeoutMs,
    buildArgs: () => ["status"],
  }),
  scriptTool({
    name: "parttime_sign_in",
    description: "Sign in for a part-time attendance project period. This records a real attendance timestamp once committed. Set dry_run=true to stop at the confirmation page without committing anything. Requires confirm=true.",
    inputSchema: {
      row: z.number().int().min(0).optional().describe("Row index from parttime_get_status. Default: the single row with can_sign_in true."),
      dry_run: z.boolean().optional().describe("Stop after the first postback and return the confirm page text; commits nothing."),
      confirm: z.literal(true).describe("Required explicit confirmation."),
    },
    annotations: { readOnlyHint: false, destructiveHint: true, openWorldHint: true },
    script,
    requires,
    envelope,
    timeoutMs,
    buildArgs: (input) => {
      const argv = ["sign-in"];
      pushFlag(argv, "--row", input.row);
      pushFlag(argv, "--dry-run", input.dry_run);
      argv.push("--yes");
      return argv;
    },
  }),
  scriptTool({
    name: "parttime_sign_out",
    description: "Sign out for a part-time attendance project period. This records a real attendance timestamp once committed. Set dry_run=true to stop at the confirmation page without committing anything. Requires confirm=true.",
    inputSchema: {
      row: z.number().int().min(0).optional().describe("Row index from parttime_get_status. Default: the single row with can_sign_out true."),
      dry_run: z.boolean().optional().describe("Stop after the first postback and return the confirm page text; commits nothing."),
      confirm: z.literal(true).describe("Required explicit confirmation."),
    },
    annotations: { readOnlyHint: false, destructiveHint: true, openWorldHint: true },
    script,
    requires,
    envelope,
    timeoutMs,
    buildArgs: (input) => {
      const argv = ["sign-out"];
      pushFlag(argv, "--row", input.row);
      pushFlag(argv, "--dry-run", input.dry_run);
      argv.push("--yes");
      return argv;
    },
  }),
];
