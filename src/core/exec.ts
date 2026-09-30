import { join } from "node:path";
import { homedir } from "node:os";
import type { ToolRunResult } from "./result.ts";
import { truncateStructured } from "./truncate.ts";

export interface RunScriptOptions {
  script: string;
  args: string[];
  envelope: boolean;
  timeoutMs: number;
  truncationHint?: string;
  env?: Record<string, string | undefined>;
}

export const DEFAULT_TRUNCATION_HINT = "narrow the request or rerun writing the full output to a file";

interface EnvelopeSuccess {
  success: true;
  data: unknown;
  metadata?: Record<string, unknown>;
}

interface EnvelopeFailure {
  success: false;
  error?: { message?: string; why?: string | null; hint?: string | null };
}

type Envelope = EnvelopeSuccess | EnvelopeFailure;

let SCRIPTS_DIR = join(import.meta.dir, "..", "..", "scripts");

export function setScriptsDirectory(directory: string): void { SCRIPTS_DIR = directory; }

export const EXEC_GRACE_MS = 2000;

function scriptPath(script: string): string {
  return join(SCRIPTS_DIR, script);
}

export function augmentedEnv(env: Record<string, string | undefined> = process.env): Record<string, string | undefined> {
  const home = homedir();
  const extra = [join(home, ".bun", "bin"), "/opt/homebrew/bin", join(home, ".local", "bin")];
  return { ...env, PATH: `${extra.join(":")}:${env.PATH ?? ""}` };
}

function isEnvelope(value: unknown): value is Envelope {
  return typeof value === "object" && value !== null && "success" in value && typeof (value as { success: unknown }).success === "boolean";
}

async function readStreamUntil(stream: ReadableStream<Uint8Array> | null, deadlineAt: number): Promise<string> {
  if (!stream) return "";

  const reader = stream.getReader();
  const decoder = new TextDecoder();
  let text = "";
  let drained = false;

  const readLoop = (async () => {
    try {
      while (true) {
        const { value, done } = await reader.read();
        if (done) {
          text += decoder.decode();
          drained = true;
          return;
        }
        text += decoder.decode(value, { stream: true });
      }
    } catch {
      // Keep partial output when cancellation or stream errors happen.
    }
  })();

  const remainingMs = Math.max(0, deadlineAt - Date.now());
  await Promise.race([readLoop, new Promise<void>((resolve) => setTimeout(resolve, remainingMs))]);

  if (!drained) reader.cancel().catch(() => {});
  return text;
}

async function waitExitedUntil(proc: { exited: Promise<number> }, deadlineAt: number): Promise<number> {
  const remainingMs = Math.max(0, deadlineAt - Date.now());
  return Promise.race([proc.exited, new Promise<number>((resolve) => setTimeout(() => resolve(-1), remainingMs))]);
}

interface ScriptRun {
  stdout: string;
  stderr: string;
  exitCode: number;
  timedOut: boolean;
  timeoutMs: number;
  argv0: string;
}

/** Cap string leaves so one call cannot swallow the caller's context; records what was cut. */
function finalize(result: ToolRunResult, hint: string): ToolRunResult {
  const { value, truncation } = truncateStructured(result.structuredContent, hint);
  if (!truncation) return result;
  return { isError: result.isError, structuredContent: { ...value, _truncation: truncation } };
}

export function mapScriptOutput(envelope: boolean, run: ScriptRun, truncationHint: string = DEFAULT_TRUNCATION_HINT): ToolRunResult {
  return finalize(mapRawScriptOutput(envelope, run), truncationHint);
}

/**
 * Every failure of an envelope-speaking script reports the same shape.
 *
 * This is not cosmetic: the MCP client validates structuredContent against the
 * declared outputSchema even when isError is set, so a one-off failure shape
 * turns a readable error into a protocol error.
 */
function envelopeFailure(message: string, why: string | null, hint: string | null): ToolRunResult {
  return { isError: true, structuredContent: { error: { message, why, hint } } };
}

function streamTail(run: ScriptRun): string | null {
  const tail = [run.stderr.trim(), run.stdout.trim()].filter(Boolean).join("\n").slice(-800);
  return tail || null;
}

function mapRawScriptOutput(envelope: boolean, run: ScriptRun): ToolRunResult {
  if (run.timedOut) {
    const message = `script '${run.argv0}' timed out after ${run.timeoutMs}ms and was killed`;
    if (envelope) {
      return envelopeFailure(message, streamTail(run), "narrow the request, or the target host may be unreachable");
    }
    return {
      isError: true,
      structuredContent: {
        stdout: run.stdout,
        stderr: run.stderr,
        exit_code: run.exitCode,
        timed_out: true,
        message,
      },
    };
  }

  if (!envelope) {
    return {
      isError: run.exitCode !== 0,
      structuredContent: { stdout: run.stdout, stderr: run.stderr, exit_code: run.exitCode },
    };
  }

  let parsed: unknown;
  try {
    parsed = JSON.parse(run.stdout);
  } catch {
    return envelopeFailure(
      `script '${run.argv0}' produced no JSON envelope (exit ${run.exitCode})`,
      streamTail(run),
      "the script likely crashed before emitting output; run it directly to see the traceback",
    );
  }

  if (!isEnvelope(parsed)) {
    return envelopeFailure(
      `script '${run.argv0}' emitted JSON that is not an envelope (exit ${run.exitCode})`,
      streamTail(run),
      "the script must emit {success, data, metadata} or {success: false, error}",
    );
  }

  if (parsed.success) {
    return {
      isError: false,
      structuredContent: { data: parsed.data, metadata: parsed.metadata ?? {} },
    };
  }

  const error = parsed.error ?? {};
  return envelopeFailure(error.message ?? "script reported failure with no error detail", error.why ?? null, error.hint ?? null);
}

export async function runScript(options: RunScriptOptions): Promise<ToolRunResult> {
  const argv = [scriptPath(options.script), ...options.args];
  const timeoutMs = options.timeoutMs;
  let proc;
  try {
    proc = Bun.spawn(argv, {
      stdio: ["ignore", "pipe", "pipe"],
      env: options.env ?? augmentedEnv(),
    });
  } catch (error) {
    return envelopeFailure("Could not start the NYCU script", String(error), "Install uv, then check the plugin installation.");
  }

  let timedOut = false;
  const timer = setTimeout(() => {
    timedOut = true;
    proc.kill();
  }, timeoutMs);
  const deadlineAt = Date.now() + timeoutMs + EXEC_GRACE_MS;

  try {
    const [stdout, stderr, exitCode] = await Promise.all([readStreamUntil(proc.stdout, deadlineAt), readStreamUntil(proc.stderr, deadlineAt), waitExitedUntil(proc, deadlineAt)]);
    return mapScriptOutput(options.envelope, { stdout, stderr, exitCode, timedOut, timeoutMs, argv0: argv[0]! }, options.truncationHint ?? DEFAULT_TRUNCATION_HINT);
  } finally {
    clearTimeout(timer);
  }
}
