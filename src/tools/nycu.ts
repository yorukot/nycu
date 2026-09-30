import { z } from "zod";
import { pushFlag } from "../core/argv.ts";
import { scriptTool, type ToolboxTool } from "../core/tool.ts";

const script = "nycu.py";
/** Reads/writes macOS Keychain items and drives headless Chromium; only makes sense on Loki's Mac. */
const requires = ["platform:darwin", "binary:uv"];
const envelope = true;
const timeoutMs = 60000;

const read = { readOnlyHint: true, openWorldHint: true } as const;

export const nycuTools: ToolboxTool[] = [
  scriptTool({
    name: "nycu_setup",
    description: "Install the headless Chromium build the NYCU portal login needs. Run once before the first nycu_whoami/parttime call on a fresh machine.",
    inputSchema: {},
    annotations: { readOnlyHint: false, destructiveHint: false, openWorldHint: true },
    script,
    requires,
    envelope,
    timeoutMs: 120000,
    buildArgs: () => ["setup"],
  }),
  scriptTool({
    name: "nycu_whoami",
    description: "Show the authenticated NYCU portal user. Logs in via headless Chromium the first time (about 20s); later calls reuse the cached token.",
    inputSchema: {},
    annotations: read,
    script,
    requires,
    envelope,
    timeoutMs,
    buildArgs: () => ["whoami"],
  }),
  scriptTool({
    name: "nycu_list_systems",
    description: "List NYCU portal sub-systems reachable through SSO. Use query to find the sysDirect value a specific service (e.g. the part-time timeclock) needs.",
    inputSchema: { query: z.string().optional().describe("Case-insensitive substring match on name/name_en/direct.") },
    annotations: read,
    script,
    requires,
    envelope,
    timeoutMs,
    buildArgs: (input) => {
      const argv = ["systems"];
      pushFlag(argv, "--query", input.query);
      return argv;
    },
  }),
  scriptTool({
    name: "nycu_list_events",
    description: "List upcoming NYCU portal events for the authenticated user.",
    inputSchema: {},
    annotations: read,
    script,
    requires,
    envelope,
    timeoutMs,
    buildArgs: () => ["events"],
  }),
  scriptTool({
    name: "nycu_logout",
    description: "Forget the cached portal JWT. Keychain credentials are untouched, so the next call just logs in again; this only clears the current session cache. Requires confirm=true.",
    inputSchema: { confirm: z.literal(true).describe("Required explicit confirmation.") },
    annotations: { readOnlyHint: false, destructiveHint: true, openWorldHint: false },
    script,
    requires,
    envelope,
    timeoutMs,
    buildArgs: () => ["logout", "--yes"],
  }),
];
