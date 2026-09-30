/**
 * Context-budget guard for tool output.
 *
 * Only string leaves are shortened. Structure, types and array lengths survive
 * untouched, so truncation can never turn a result into an outputSchema
 * violation, and a caller is never silently handed a shorter list than the tool
 * actually produced.
 */

export interface TruncationReport {
  fields: string[];
  original_chars: number;
  limit: number;
}

export const DEFAULT_MAX_STRING_CHARS = 20_000;
export const DEFAULT_MAX_TOTAL_CHARS = 120_000;
const MIN_STRING_CAP = 200;

export interface TruncateLimits {
  maxStringChars: number;
  maxTotalChars: number;
}

function readEnvInt(name: string, fallback: number): number {
  const raw = process.env[name] ?? process.env[name.replace(/^NYCU_/, "UTILS_")];
  if (raw === undefined) return fallback;
  const parsed = Number.parseInt(raw, 10);
  return Number.isFinite(parsed) && parsed > 0 ? parsed : fallback;
}

export function limitsFromEnv(): TruncateLimits {
  return {
    maxStringChars: readEnvInt("NYCU_MCP_MAX_STRING_CHARS", DEFAULT_MAX_STRING_CHARS),
    maxTotalChars: readEnvInt("NYCU_MCP_MAX_TOTAL_CHARS", DEFAULT_MAX_TOTAL_CHARS),
  };
}

function marker(kept: number, original: number, hint: string): string {
  const dropped = original - kept;
  return `\n…[truncated ${dropped} of ${original} chars${hint ? ` — ${hint}` : ""}]`;
}

function serializedLength(value: unknown): number {
  try {
    return JSON.stringify(value)?.length ?? 0;
  } catch {
    return 0;
  }
}

function countStringLeaves(value: unknown): number {
  if (typeof value === "string") return 1;
  if (Array.isArray(value)) return value.reduce<number>((sum, item) => sum + countStringLeaves(item), 0);
  if (value !== null && typeof value === "object") {
    return Object.values(value as Record<string, unknown>).reduce<number>((sum, item) => sum + countStringLeaves(item), 0);
  }
  return 0;
}

interface WalkState {
  cap: number;
  hint: string;
  fields: string[];
}

function walk(value: unknown, path: string, state: WalkState): unknown {
  if (typeof value === "string") {
    if (value.length <= state.cap) return value;
    state.fields.push(path || "<root>");
    return value.slice(0, state.cap) + marker(state.cap, value.length, state.hint);
  }

  if (Array.isArray(value)) {
    return value.map((item, index) => walk(item, `${path}[${index}]`, state));
  }

  if (value !== null && typeof value === "object") {
    const entries = Object.entries(value as Record<string, unknown>);
    return Object.fromEntries(entries.map(([key, item]) => [key, walk(item, path ? `${path}.${key}` : key, state)]));
  }

  return value;
}

/**
 * Shorten string leaves so a single call cannot swallow the caller's context.
 *
 * Pass 1 caps every string at `maxStringChars`. If the serialized result is
 * still over `maxTotalChars`, pass 2 recomputes a per-string cap by dividing the
 * total budget across the string leaves (floor `MIN_STRING_CAP`) and re-walks
 * the original value once. Deterministic, at most two walks, never iterative.
 */
export function truncateStructured(
  value: Record<string, unknown>,
  hint: string,
  limits: TruncateLimits = limitsFromEnv(),
): { value: Record<string, unknown>; truncation?: TruncationReport } {
  const originalChars = serializedLength(value);

  const firstPass: WalkState = { cap: limits.maxStringChars, hint, fields: [] };
  let result = walk(value, "", firstPass) as Record<string, unknown>;
  let state = firstPass;

  if (serializedLength(result) > limits.maxTotalChars) {
    const leaves = countStringLeaves(value);
    const cap = leaves > 0 ? Math.max(MIN_STRING_CAP, Math.floor(limits.maxTotalChars / leaves)) : limits.maxStringChars;
    if (cap < limits.maxStringChars) {
      const secondPass: WalkState = { cap, hint, fields: [] };
      result = walk(value, "", secondPass) as Record<string, unknown>;
      state = secondPass;
    }
  }

  if (state.fields.length === 0) return { value: result };

  return {
    value: result,
    truncation: { fields: state.fields, original_chars: originalChars, limit: state.cap },
  };
}
