import { describe, expect, test } from "bun:test";
import { truncateStructured } from "../src/core/truncate.ts";

const limits = { maxStringChars: 100, maxTotalChars: 1000 };
const hint = "pass out=<path>";

describe("string-leaf truncation", () => {
  test("leaves output under the cap untouched", () => {
    const value = { data: { text: "short" }, metadata: { count: 1 } };
    const result = truncateStructured(value, hint, limits);

    expect(result.truncation).toBeUndefined();
    expect(result.value).toEqual(value);
  });

  test("cuts a long string and names both the loss and the escape hatch", () => {
    const value = { data: { text: "x".repeat(500) } };
    const result = truncateStructured(value, hint, limits);

    const text = (result.value.data as { text: string }).text;
    expect(text.startsWith("x".repeat(100))).toBe(true);
    expect(text).toContain("truncated 400 of 500 chars");
    expect(text).toContain(hint);
    expect(result.truncation?.fields).toEqual(["data.text"]);
    expect(result.truncation?.limit).toBe(100);
  });

  test("preserves structure, types and array length", () => {
    const value = {
      data: {
        rows: [
          { id: 1, body: "y".repeat(300) },
          { id: 2, body: "short" },
          { id: 3, body: "z".repeat(300) },
        ],
        flag: true,
        nothing: null,
      },
      metadata: { count: 3 },
    };
    const result = truncateStructured(value, hint, limits);
    const data = result.value.data as { rows: Array<{ id: number; body: string }>; flag: boolean; nothing: null };

    expect(data.rows).toHaveLength(3);
    expect(data.rows.map((row) => row.id)).toEqual([1, 2, 3]);
    expect(data.rows[1]!.body).toBe("short");
    expect(data.flag).toBe(true);
    expect(data.nothing).toBeNull();
    expect(result.truncation?.fields).toEqual(["data.rows[0].body", "data.rows[2].body"]);
  });

  test("records the pre-truncation size", () => {
    const value = { data: "w".repeat(400) };
    const result = truncateStructured(value, hint, limits);

    expect(result.truncation?.original_chars).toBe(JSON.stringify(value).length);
  });

  test("refuses to shred strings when the total budget cannot be divided usefully", () => {
    // 40 leaves against a 1000-char total gives 25 chars each, below the 200 floor.
    // Rather than return 40 useless fragments, pass 2 declines and the output stays over budget.
    const value = { data: Array.from({ length: 40 }, () => "q".repeat(100)) };
    const result = truncateStructured(value, hint, { maxStringChars: 100, maxTotalChars: 1000 });

    expect(result.truncation).toBeUndefined();
    expect((result.value.data as string[])[0]).toBe("q".repeat(100));
  });

  test("second pass shortens strings when the floor allows it", () => {
    const value = { data: Array.from({ length: 10 }, () => "q".repeat(5000)) };
    const result = truncateStructured(value, hint, { maxStringChars: 4000, maxTotalChars: 10_000 });

    expect(result.truncation?.limit).toBe(1000);
    expect((result.value.data as string[])[0]!.startsWith("q".repeat(1000))).toBe(true);
    expect(result.truncation?.fields).toHaveLength(10);
  });

  test("handles raw stdout/stderr shells too", () => {
    const value = { stdout: "s".repeat(400), stderr: "", exit_code: 0 };
    const result = truncateStructured(value, hint, limits);

    expect(result.value.exit_code).toBe(0);
    expect(result.value.stderr).toBe("");
    expect(result.truncation?.fields).toEqual(["stdout"]);
  });
});
