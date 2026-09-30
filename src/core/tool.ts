import type { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import type { ToolAnnotations } from "@modelcontextprotocol/sdk/types.js";
import { z } from "zod";
import { augmentedEnv, runScript, type RunScriptOptions } from "./exec.ts";
import { evaluateRequirements, type EvaluateOptions } from "./requires.ts";
import { mcpResult, type ToolRunResult } from "./result.ts";
import { envelopeOutput, rawOutput } from "./schema.ts";

export interface ToolboxTool {
  name: string;
  description: string;
  inputSchema: z.ZodRawShape;
  outputSchema: z.ZodRawShape;
  annotations: ToolAnnotations;
  /** Host requirements (see core/requires.ts). Unmet means the tool is not registered at all. */
  requires?: string[];
  run(args: Record<string, unknown>): Promise<ToolRunResult>;
}

export interface ScriptToolDefinition<Shape extends z.ZodRawShape> {
  name: string;
  description: string;
  inputSchema: Shape;
  /** Omit for the default envelope/raw shell; supply a Tier A shape only once real output has been verified. */
  outputSchema?: z.ZodRawShape;
  /** Required, so a new tool cannot be added without declaring whether it reads or writes. */
  annotations: ToolAnnotations;
  /** Host requirements (see core/requires.ts); usually one const shared by the whole domain. */
  requires?: string[];
  script: string;
  envelope: boolean;
  timeoutMs: number;
  /** Appended to the truncation marker; name the escape hatch, e.g. writing to a file. */
  truncationHint?: string;
  buildArgs(input: z.infer<z.ZodObject<Shape>>): string[];
  buildEnv?(input: z.infer<z.ZodObject<Shape>>): RunScriptOptions["env"];
}

export function scriptTool<const Shape extends z.ZodRawShape>(definition: ScriptToolDefinition<Shape>): ToolboxTool {
  const schema = z.object(definition.inputSchema);
  const outputSchema = definition.outputSchema ?? (definition.envelope ? envelopeOutput() : rawOutput());

  return {
    name: definition.name,
    description: definition.description,
    inputSchema: definition.inputSchema,
    outputSchema,
    annotations: definition.annotations,
    requires: definition.requires,
    async run(args) {
      const input = schema.parse(args);
      return runScript({
        script: definition.script,
        args: definition.buildArgs(input),
        envelope: definition.envelope,
        timeoutMs: definition.timeoutMs,
        truncationHint: definition.truncationHint,
        env: definition.buildEnv ? { ...augmentedEnv(), ...definition.buildEnv(input) } : undefined,
      });
    },
  };
}

export function registerTools(server: McpServer, tools: ToolboxTool[]): void {
  const names = new Set<string>();

  for (const tool of tools) {
    if (names.has(tool.name)) {
      throw new Error(`duplicate MCP tool name: ${tool.name}`);
    }
    names.add(tool.name);

    server.registerTool(
      tool.name,
      {
        description: tool.description,
        inputSchema: tool.inputSchema,
        outputSchema: tool.outputSchema,
        annotations: tool.annotations,
      },
      async (args) => mcpResult(await tool.run(args as Record<string, unknown>)),
    );
  }
}

export interface HiddenTool {
  tool: string;
  failed: string[];
}

export interface HostSelection {
  registered: ToolboxTool[];
  hidden: HiddenTool[];
}

/**
 * Split a toolset by what this host can actually run.
 *
 * All requirements are probed in parallel and memoised per requirement string,
 * so a whole domain sharing `binary:uv` costs one probe. A failing probe hides its
 * tool; it never rejects.
 */
export async function selectRunnableTools(tools: ToolboxTool[], options: EvaluateOptions = {}): Promise<HostSelection> {
  const results = await Promise.all(tools.map((tool) => evaluateRequirements(tool.requires ?? [], options)));

  const registered: ToolboxTool[] = [];
  const hidden: HiddenTool[] = [];

  tools.forEach((tool, index) => {
    const result = results[index]!;
    if (result.ok) registered.push(tool);
    else hidden.push({ tool: tool.name, failed: result.failed });
  });

  return { registered, hidden };
}

/** One entry per distinct family/reason pair, e.g. `safari: platform:darwin`, for the startup log. */
export function summariseHidden(hidden: HiddenTool[]): string {
  const reasons = new Set<string>();

  for (const entry of hidden) {
    const family = entry.tool.split("_")[0] ?? entry.tool;
    reasons.add(`${family}: ${entry.failed.join(", ") || "unknown"}`);
  }

  return [...reasons].join(", ");
}
