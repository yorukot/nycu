import { z } from "zod";
import { pushFlag } from "../core/argv.ts";
import { scriptTool, type ToolboxTool } from "../core/tool.ts";

const script = "timetable.py";
/** Pure HTTP against timetable.nycu.edu.tw; the only host dependency is the uv runtime. */
const requires = ["binary:uv"];
const envelope = true;
const timeoutMs = 60000;

/** Every call hits the public, unauthenticated NYCU timetable API. */
const read = { readOnlyHint: true, openWorldHint: true } as const;

export const timetableTools: ToolboxTool[] = [
  scriptTool({
    name: "timetable_list_semesters",
    description: "List NYCU timetable semester codes (e.g. 1151), newest first. Pass one as acysem to search/lookup, or omit it there to default to the latest semester.",
    inputSchema: {},
    annotations: read,
    script,
    requires,
    envelope,
    timeoutMs,
    buildArgs: () => ["semesters"],
  }),
  scriptTool({
    name: "timetable_search_courses",
    description: "Search NYCU course meeting times by course name, teacher, or course code. Returns day/period/room per course. Broad Chinese-name queries return many cross-department duplicates; narrow with by=code or an explicit acysem.",
    inputSchema: {
      query: z.string().describe("Search text: course name, teacher name, or course code, depending on `by`."),
      by: z.enum(["name", "teacher", "code"]).optional().describe("Field to search. Default: name."),
      acysem: z.string().optional().describe("Semester code like 1151. Default: latest semester."),
    },
    annotations: read,
    script,
    requires,
    envelope,
    timeoutMs,
    truncationHint: "narrow with by=code, or add acysem to scope one semester",
    buildArgs: (input) => {
      const argv = ["search", input.query];
      pushFlag(argv, "--by", input.by);
      pushFlag(argv, "--acysem", input.acysem);
      return argv;
    },
  }),
  scriptTool({
    name: "timetable_lookup_courses",
    description: "Look up class meeting time (day/period/room) for specific course IDs, max 20 per call. cos_id is the part after the dot in an E3 shortname like 1151.535702; chains directly off e3p_list_courses output. Unmatched IDs are reported, not treated as an error.",
    inputSchema: {
      cos_ids: z.array(z.union([z.string(), z.number()])).max(20).describe("One or more course IDs (cos_id), e.g. 535702. Max 20 per call."),
      acysem: z.string().optional().describe("Semester code like 1151. Default: latest semester."),
    },
    annotations: read,
    script,
    requires,
    envelope,
    timeoutMs,
    buildArgs: (input) => {
      const argv = ["lookup", ...input.cos_ids.map(String)];
      pushFlag(argv, "--acysem", input.acysem);
      return argv;
    },
  }),
  scriptTool({
    name: "timetable_get_periods",
    description: "Static reference table mapping NYCU period codes (1-9, y, z, n, a-d) to start/end times, plus day letters to weekday names. No network call.",
    inputSchema: {},
    annotations: read,
    script,
    requires,
    envelope,
    timeoutMs,
    buildArgs: () => ["periods"],
  }),
];
