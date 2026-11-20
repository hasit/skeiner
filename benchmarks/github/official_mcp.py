"""Run GitHub's pinned native MCP server with only the read-only list_commits tool."""

import json
import os
from pathlib import Path
from queue import Queue, Empty
import subprocess
from threading import Thread
import time


VERSION = "2.2.0"
COMMIT = "67d4dc489700ec955ad255d098396dd86c729523"
DEFAULT_BINARY = Path(__file__).resolve().parents[2] / "bin" / f"github-mcp-v{VERSION}" / "github-mcp-server"
TOOL = "list_commits"


def github_token():
    """Use a supplied GitHub token or the active github.com gh login."""
    for variable in ("GITHUB_TOKEN", "GH_TOKEN", "GITHUB_PERSONAL_ACCESS_TOKEN"):
        if os.environ.get(variable):
            return os.environ[variable]
    try:
        result = subprocess.run(
            ["gh", "auth", "token", "--hostname", "github.com"],
            capture_output=True, text=True, timeout=10, check=True,
        )
    except (FileNotFoundError, subprocess.SubprocessError) as exc:
        raise RuntimeError("Set GITHUB_TOKEN or authenticate gh for github.com") from exc
    token = result.stdout.strip()
    if not token:
        raise RuntimeError("GitHub CLI returned an empty token for github.com")
    return token


class OfficialGitHub:
    def __init__(self, binary, token, csv_output=False):
        self.binary = str(Path(binary).resolve())
        self.token = token
        self.csv_output = csv_output
        self.next_id = 0
        self.messages = Queue()

    def __enter__(self):
        build = subprocess.run([self.binary, "--version"], env={}, capture_output=True,
                               text=True, timeout=10, check=True).stdout.splitlines()
        if f"Version: {VERSION}" not in build or f"Commit: {COMMIT}" not in build:
            raise RuntimeError(f"Use the official github-mcp-server v{VERSION} binary")
        command = [self.binary, "stdio", "--read-only", "--tools", TOOL]
        if self.csv_output:
            command.extend(["--features", "csv_output"])
        self.child = subprocess.Popen(
            command, env={"GITHUB_PERSONAL_ACCESS_TOKEN": self.token},
            stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
            text=True, encoding="utf-8",
        )
        def receive():
            try:
                for line in self.child.stdout:
                    self.messages.put(line)
            finally:
                self.messages.put(None)
        self.reader = Thread(target=receive, daemon=True)
        self.reader.start()
        try:
            metadata = self._request("initialize", {
                "protocolVersion": "2025-06-18", "capabilities": {},
                "clientInfo": {"name": "skeiner-github-benchmark", "version": "1"},
            })
            self.server_info = metadata["serverInfo"]
            if self.server_info.get("name") != "github-mcp-server" or self.server_info.get("version") != VERSION:
                raise RuntimeError("Unexpected GitHub MCP server identity")
            self._send({"jsonrpc": "2.0", "method": "notifications/initialized", "params": {}})
            catalog = self._request("tools/list", {})["tools"]
            if [tool["name"] for tool in catalog] != [TOOL] or not catalog[0]["annotations"].get("readOnlyHint"):
                raise RuntimeError("GitHub MCP did not advertise the requested read-only catalog")
            self.tool_schema = catalog[0]["inputSchema"]
        except BaseException:
            self._close()
            raise
        return self

    def _send(self, message):
        self.child.stdin.write(json.dumps(message) + "\n")
        self.child.stdin.flush()

    def _request(self, method, params):
        self.next_id += 1
        self._send({"jsonrpc": "2.0", "id": self.next_id, "method": method, "params": params})
        deadline = time.monotonic() + 60
        while True:
            try:
                line = self.messages.get(timeout=max(0, deadline - time.monotonic()))
            except Empty:
                raise TimeoutError(f"GitHub MCP timed out during {method}") from None
            if line is None:
                raise RuntimeError(f"GitHub MCP closed stdio during {method}")
            message = json.loads(line)
            if message.get("id") == self.next_id:
                if "error" in message:
                    raise RuntimeError(f"GitHub MCP rejected {method} (code {message['error']['code']})")
                return message["result"]

    def call(self, arguments):
        """Return the original, unmodified MCP text content for byte counting."""
        result = self._request("tools/call", {"name": TOOL, "arguments": arguments})
        if result.get("isError"):
            raise RuntimeError("GitHub list_commits failed. Check the GitHub token, repo, and rate limit.")
        blocks = result["content"]
        if len(blocks) != 1 or blocks[0]["type"] != "text":
            raise RuntimeError("Expected one GitHub MCP text result")
        return blocks[0]["text"]

    def _close(self):
        try:
            self.child.stdin.close()
        except BrokenPipeError:
            pass
        try:
            self.child.wait(timeout=5)
        except subprocess.TimeoutExpired:
            self.child.kill()
            self.child.wait()
        self.reader.join()
        self.child.stdout.close()

    def __exit__(self, *unused):
        self._close()
