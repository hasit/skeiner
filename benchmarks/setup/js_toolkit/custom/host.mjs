import { Server } from "@modelcontextprotocol/sdk/server/index.js";
import { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";
import { CallToolRequestSchema, ListToolsRequestSchema } from "@modelcontextprotocol/sdk/types.js";
import { parse as parseYaml } from "yaml";
import { z } from "zod";

// Load only the fixed dependency graph as the trusted Deno entry point.
// Authored code is evaluated at runtime under the runner's permissions.
const source = await Deno.readTextFile(Deno.args[0]);
const libraries = { McpServer, Server, StdioServerTransport, ListToolsRequestSchema, CallToolRequestSchema, parseYaml, z };
const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor;
await new AsyncFunction(...Object.keys(libraries), source)(...Object.values(libraries));
