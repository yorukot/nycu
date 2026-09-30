import { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";
import { fileURLToPath } from "node:url";
import { registerTools, selectRunnableTools, summariseHidden } from "./core/tool.ts";
import { setScriptsDirectory } from "./core/exec.ts";
import { allTools } from "./tools/index.ts";
import manifest from "../plugin.json";

setScriptsDirectory(fileURLToPath(new URL("../scripts/", import.meta.url)));
const server = new McpServer({ name: manifest.name, version: manifest.version });
const { registered, hidden } = await selectRunnableTools(allTools);
registerTools(server, registered);
console.error(`[nycu-mcp] registered ${registered.length}, hidden ${hidden.length}${hidden.length ? ` (${summariseHidden(hidden)})` : ""}`);
await server.connect(new StdioServerTransport());
