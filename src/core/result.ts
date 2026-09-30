export interface ToolRunResult {
  structuredContent: Record<string, unknown>;
  isError: boolean;
}

export function mcpResult(result: ToolRunResult) {
  return {
    content: [{ type: "text" as const, text: JSON.stringify(result.structuredContent) }],
    structuredContent: result.structuredContent,
    isError: result.isError,
  };
}
