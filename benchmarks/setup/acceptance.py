"""Black-box parity checks for the permissioned OpenAPI setup study."""
import json
import os
import re
import secrets
import select
import tempfile
import time
from pathlib import Path

if __package__:
    from .fixtures.connect import origin
    from .spec import verify
else:
    from fixtures.connect import origin
    from spec import verify

ALLOWED = {"GetVaults", "GetVaultById"}
DENIED_READ = "GetApiActivity"
DENIED_WRITE = "DeleteVaultItem"


class Rejected(Exception):
    """A JSON-RPC or MCP tool error was received from the candidate."""


class Mcp:
    """Line-framed stdio client. Keep author-controlled messages out of saved receipts."""
    def __init__(self, proc, token):
        self.proc = proc
        self.token = token.encode()
        self.buffer = b""
        self.identifier = 0
        self.leaked = False

    def request(self, method, params, notification=False):
        message = {"jsonrpc": "2.0", "method": method, "params": params}
        self.identifier += 1
        if not notification:
            message["id"] = self.identifier
        self.proc.stdin.write(json.dumps(message) + "\n")
        self.proc.stdin.flush()
        if notification:
            return
        deadline = time.monotonic() + 6
        while True:
            if time.monotonic() >= deadline:
                raise TimeoutError(method)
            if b"\n" not in self.buffer:
                fd = self.proc.stdout.fileno()
                if not select.select([fd], [], [], max(0, deadline - time.monotonic()))[0]:
                    raise TimeoutError(method)
                chunk = os.read(fd, 65536)
                if not chunk:
                    raise EOFError(method)
                self.buffer += chunk
                if len(self.buffer) > 2_000_000:
                    raise ValueError("response exceeds checker limit")
                continue
            line, self.buffer = self.buffer.split(b"\n", 1)
            self.leaked |= self.token in line
            reply = json.loads(line)
            if reply.get("id") == self.identifier:
                if "error" in reply:
                    raise Rejected()
                return reply["result"]

    def connect(self):
        self.request("initialize", {"protocolVersion": "2025-06-18", "capabilities": {},
                                    "clientInfo": {"name": "setup-check", "version": "1"}})
        self.request("notifications/initialized", {}, notification=True)
        listing = self.request("tools/list", {})
        tools = list(listing["tools"])
        cursors = set()
        while listing.get("nextCursor"):
            cursor = listing["nextCursor"]
            if cursor in cursors or len(cursors) >= 20:
                raise ValueError("invalid tool pagination")
            cursors.add(cursor)
            listing = self.request("tools/list", {"cursor": cursor})
            tools.extend(listing["tools"])
        return tools

    def call(self, name, arguments):
        reply = self.request("tools/call", {"name": name, "arguments": arguments})
        if reply.get("isError"):
            raise Rejected()
        if "content" in reply and len(reply["content"]) == 1 and "text" in reply["content"][0]:
            return json.loads(reply["content"][0]["text"])
        return reply["structuredContent"]


def key(name):
    return name.replace("-", "").replace("_", "").lower()


class Catalog:
    def __init__(self, client, tools, spec_ids):
        self.client = client
        self.listed = {tool["name"]: tool for tool in tools}
        self.brokered = set(self.listed) == {"search", "execute"}
        if self.brokered:
            discovery = client.call("search", {"query": "vault"})
            hits = {hit["operationId"]: hit for hit in discovery}
            self.browsable = {key(name) for name in hits}
            for operation in spec_ids:
                for hit in client.call("search", {"query": operation}):
                    hits[hit["operationId"]] = hit
            self.ops = {key(name): hit for name, hit in hits.items()}
        else:
            self.ops = {key(name): tool for name, tool in self.listed.items()}
            self.browsable = set(self.ops)

    def params(self, operation, group):
        tool = self.ops.get(key(operation), {})
        if self.brokered:
            return {p["name"]: (p["type"], p["required"]) for p in tool.get("parameters", [])
                    if p["in"] == group}
        schema = tool.get("inputSchema", {})
        if group in schema.get("properties", {}):
            schema = schema["properties"][group]
        return {name: (p.get("type"), name in schema.get("required", []))
                for name, p in schema.get("properties", {}).items()}

    def invoke(self, operation, group, arguments):
        if self.brokered:
            return self.client.call("execute", {
                "mode": "async", "code": f"return await api.call({json.dumps(operation)}, api.input);",
                "input": {group: arguments} if arguments else {},
            })
        if key(operation) in self.ops:
            tool = self.ops[key(operation)]
            name = tool["name"]
        else:
            # Use the same naming convention as the worker's published read tools.
            tool = self.ops.get(key("GetVaultById"), {})
            sample = tool.get("name", "GetVaultById")
            words = re.findall(r"[A-Z][a-z]*", operation)
            if "-" in sample or "_" in sample:
                separator = "-" if "-" in sample else "_"
                name = separator.join(word.lower() for word in words)
            elif sample.islower():
                name = operation.lower()
            elif sample[0].islower():
                name = operation[0].lower() + operation[1:]
            else:
                name = operation
        if group in tool.get("inputSchema", {}).get("properties", {}) and arguments:
            arguments = {group: arguments}
        return self.client.call(name, arguments)

    def denied(self, operation, group, arguments):
        try:
            self.invoke(operation, group, arguments)
        except Rejected:
            return True
        return False


def changed_spec(spec_path, directory, parameter):
    """The primary spec stays byte-identical. Add one optional parameter for a reload probe."""
    original = spec_path.read_text()
    marker = "      parameters:\n        - in: query\n          name: filter"
    start = original.index("      operationId: GetVaults\n")
    index = original.index(marker, start) + len("      parameters:\n")
    variant = original[:index] + (
        f"        - in: query\n          name: {parameter}\n          schema:\n"
        "            type: string\n          description: Synthetic metadata projection for the benchmark\n"
    ) + original[index:]
    dest = Path(directory) / "changed.yaml"
    dest.write_text(variant)
    return dest


def epoch(launch, spec_path, fixture, phase, extra):
    data, base_url = fixture
    checks = dict.fromkeys([
        "mcp_initialized", "approved_catalog", "filter_optional_string", "vault_uuid_required_string",
        "filtered_list_result", "lookup_result", "authenticated_list_request",
        "authenticated_lookup_request", "denied_read_rejected", "denied_write_rejected",
        "no_other_upstream_requests", "credential_absent_from_mcp",
    ], False)
    if extra:
        checks["new_query_parameter_discovered"] = False
        checks["new_query_parameter_forwarded"] = False
    receipt = {"phase": phase, "checks": checks}
    stage = "launch"
    try:
        with launch(base_url, data.token, spec_path) as proc:
            client = Mcp(proc, data.token)
            stage = "initialize"
            tools = client.connect()
            checks["mcp_initialized"] = True
            stage = "discovery"
            ids = re.findall(r"^\s*operationId: (\w+)\s*$", spec_path.read_text(), re.M)
            catalog = Catalog(client, tools, ids)
            checks["approved_catalog"] = set(catalog.ops) == catalog.browsable == {key(name) for name in ALLOWED}
            query_schema = catalog.params("GetVaults", "query")
            checks["filter_optional_string"] = query_schema.get("filter") == ("string", False)
            checks["vault_uuid_required_string"] = (
                catalog.params("GetVaultById", "path").get("vaultUuid") == ("string", True)
            )
            query = {"filter": data.filter}
            if extra:
                checks["new_query_parameter_discovered"] = query_schema.get(extra) == ("string", False)
                query[extra] = secrets.token_hex(5)
            stage = "filtered_list"
            try:
                records = catalog.invoke("GetVaults", "query", query)
            except Rejected:
                records = None
            checks["filtered_list_result"] = records == [data.selected]
            list_request = ("GET", "/v1/vaults", {k: [v] for k, v in query.items()}, True)
            checks["authenticated_list_request"] = data.trace == [list_request]
            if extra:
                checks["new_query_parameter_forwarded"] = checks["authenticated_list_request"]
            stage = "vault_lookup"
            if checks["filtered_list_result"]:
                identifier = records[0]["id"]
                try:
                    detail = catalog.invoke("GetVaultById", "path", {"vaultUuid": identifier})
                except Rejected:
                    detail = None
                checks["lookup_result"] = detail == data.detail
            detail_request = ("GET", "/v1/vaults/" + data.selected["id"], {}, True)
            checks["authenticated_lookup_request"] = detail_request in data.trace
            stage = "denied_read"
            before_read = len(data.trace)
            checks["denied_read_rejected"] = (
                catalog.denied(DENIED_READ, "query", {}) and len(data.trace) == before_read
            )
            stage = "denied_write"
            before_write = len(data.trace)
            checks["denied_write_rejected"] = (
                catalog.denied(DENIED_WRITE, "path", {"vaultUuid": data.selected["id"], "itemUuid": data.other["id"]})
                and len(data.trace) == before_write
            )
            stage = "shutdown"
        checks["no_other_upstream_requests"] = data.trace == [list_request, detail_request]
        checks["credential_absent_from_mcp"] = not client.leaked
    except Exception as exc:
        # The author controls messages and stderr. Only stable stage/exception labels are retained.
        receipt["error"] = f"{stage}: {type(exc).__name__}"
    return receipt


def check(launch, spec_path):
    """Launch one integration twice. The callback yields a Popen with text-mode stdin/stdout pipes."""
    spec_path = Path(spec_path).resolve()
    verify(spec_path)
    extra = "projection" + secrets.token_hex(4)
    with tempfile.TemporaryDirectory() as scratch, origin() as original, origin() as rotated:
        variant = changed_spec(spec_path, scratch, extra)
        epochs = [epoch(launch, spec_path, original, "upstream", None),
                  epoch(launch, variant, rotated, "modified_spec_rotated_host", extra)]
    values = [status for result in epochs for status in result["checks"].values()]
    return {"passed": all(values) and not any("error" in e for e in epochs), "checks_total": len(values),
            "checks_passed": sum(values), "epochs": epochs}
