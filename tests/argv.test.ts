import { describe, expect, test } from "bun:test";
import type { ToolboxTool } from "../src/core/tool.ts";
import { allTools } from "../src/tools/index.ts";
function getTool(name: string): ToolboxTool {
  const tool = allTools.find((candidate) => candidate.name === name);
  if (!tool) throw new Error(`missing test tool ${name}`);
  return tool;
}

async function runCaptured(name: string, input: Record<string, unknown>): Promise<{ argv: string[]; env?: Record<string, string | undefined> }> {
  const tool = getTool(name);
  let captured: string[] | undefined;
  let capturedEnv: Record<string, string | undefined> | undefined;
  const original = Bun.spawn;
  Bun.spawn = ((argv: string[], options?: { env?: Record<string, string | undefined> }) => {
    captured = argv.slice(1);
    capturedEnv = options?.env;
    return {
      stdout: new ReadableStream({ start(controller) { controller.close(); } }),
      stderr: new ReadableStream({ start(controller) { controller.close(); } }),
      exited: Promise.resolve(0),
      kill() {},
    };
  }) as typeof Bun.spawn;

  try {
    await tool.run(input);
    if (!captured) throw new Error("argv was not captured");
    return { argv: captured, env: capturedEnv };
  } finally {
    Bun.spawn = original;
  }
}

async function expectRejected(name: string, input: Record<string, unknown>): Promise<void> {
  const original = Bun.spawn;
  Bun.spawn = (() => {
    throw new Error("spawn should not be reached");
  }) as typeof Bun.spawn;

  try {
    await expect(getTool(name).run(input)).rejects.toThrow();
  } finally {
    Bun.spawn = original;
  }
}

describe("NYCU confirmation and argv compatibility", () => {
  test("attendance gates run before any process starts", async () => {
    for (const name of ["parttime_sign_in", "parttime_sign_out", "nycu_logout", "e3p_logout"]) {
      await expectRejected(name, {});
      await expectRejected(name, { confirm: false });
    }
  });
  test("attendance preserves row zero and dry-run confirmation flags", async () => {
    expect((await runCaptured("parttime_sign_in", { row: 0, dry_run: true, confirm: true })).argv).toEqual(["sign-in", "--row", "0", "--dry-run", "--yes"]);
    expect((await runCaptured("parttime_sign_out", { row: 2, confirm: true })).argv).toEqual(["sign-out", "--row", "2", "--yes"]);
  });
  test("E3 and timetable preserve optional scoping and mixed course IDs", async () => {
    expect((await runCaptured("e3p_list_assignments", { courseid: 123, status: true })).argv).toEqual(["assignments", "123", "--status"]);
    expect((await runCaptured("timetable_lookup_courses", { cos_ids: [123, "456"], acysem: "1151" })).argv).toEqual(["lookup", "123", "456", "--acysem", "1151"]);
  });
});
