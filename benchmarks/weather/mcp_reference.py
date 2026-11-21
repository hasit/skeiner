"""Check the public community Open-Meteo MCP against the captured daily maxima."""
import json
import re
from datetime import datetime, timezone
from time import perf_counter
from urllib.request import Request, urlopen

from dataset import CASES, expected, fetch, native, query, tls_context

URL = "https://open-meteo.caseyjhand.com/mcp"
PROTOCOL = "2025-11-25"
HISTORICAL = "openmeteo_get_historical"
QUERY = "openmeteo_dataframe_query"


def packed(value):
    return json.dumps(value, ensure_ascii=False, separators=(",", ":"), allow_nan=False).encode()


class WeatherMCP:
    """Streamable HTTP client for the hosted server's non-streaming tool calls."""

    def __init__(self):
        self.sequence = 0
        self.context = tls_context()
        self.headers = {"Accept": "application/json, text/event-stream", "Content-Type": "application/json"}

    def request(self, method, params, notification=False):
        self.sequence += 1
        payload = {"jsonrpc": "2.0", "method": method, "params": params}
        if not notification:
            payload["id"] = self.sequence
        with urlopen(Request(URL, data=packed(payload), headers=self.headers), timeout=45, context=self.context) as response:
            session = response.headers.get("Mcp-Session-Id")
            if session:
                self.headers["Mcp-Session-Id"] = session
            if notification:
                return
            if response.headers.get_content_type() == "application/json":
                messages = [json.load(response)]
            else:
                messages = (json.loads(line[6:]) for line in response if line.startswith(b"data: "))
            for message in messages:
                if message.get("id") == self.sequence:
                    if "error" in message:
                        raise RuntimeError(f"MCP {method}: {message['error']}")
                    return message["result"]
        raise RuntimeError(f"MCP {method} returned no matching response")

    def initialize(self):
        metadata = self.request("initialize", {"protocolVersion": PROTOCOL, "capabilities": {},
                                "clientInfo": {"name": "skeiner-weather-benchmark", "version": "1"}})
        self.info = metadata["serverInfo"]
        self.headers["MCP-Protocol-Version"] = metadata["protocolVersion"]
        self.request("notifications/initialized", {}, notification=True)
        self.tools = self.request("tools/list", {})["tools"]
        if not {HISTORICAL, QUERY} <= {tool["name"] for tool in self.tools}:
            raise RuntimeError("MCP no longer advertises the historical and SQL tools")

    def call(self, name, arguments):
        started = perf_counter()
        result = self.request("tools/call", {"name": name, "arguments": arguments})
        elapsed = round(1000 * (perf_counter() - started), 1)
        if result.get("isError"):
            raise RuntimeError(f"{name}: {result.get('content')}")
        text_size = sum(len(item["text"].encode()) for item in result["content"] if item["type"] == "text")
        return result, {"argument_bytes": len(packed(arguments)), "reply_text_bytes": text_size,
                        "elapsed_ms": elapsed}


def historical_args(case, sql=False):
    params = query(case)
    params["daily_variables"] = [params.pop("daily")]
    params["models"] = [params["models"]]
    if sql:
        # Daily-only results fit inline. The extra hourly series supplies enough rows to stage.
        params["hourly_variables"] = ["temperature_2m"]
    return params


def longest_spell_sql(table, threshold):
    if not re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]*", table) or type(threshold) is not int:
        raise ValueError("Unexpected stage table name or threshold")
    return f"""
SELECT min(first_day) AS start, max(last_day) AS "end",
       coalesce(max(days), 0) AS days, max(peak_c) AS peak_c
FROM (
    SELECT min(time) AS first_day, max(time) AS last_day, count(*)::INTEGER AS days,
           max(temperature_2m_max) AS peak_c
    FROM (
        SELECT time, temperature_2m_max,
               CAST(time AS DATE) - (row_number() OVER (ORDER BY time))::INTEGER AS spell
        FROM "{table}"
        WHERE temperature_2m_max >= {threshold}
    ) AS qualifying_days
    GROUP BY spell
    ORDER BY days DESC, first_day ASC
    LIMIT 1
) AS longest
""".strip()


def check(case, client, staged):
    inputs = CASES[case]
    reply, stats = client.call(HISTORICAL, historical_args(case, sql=staged))
    data = reply["structuredContent"]
    if data["timezone"] != inputs["timezone"] or data["models"] != [inputs["models"]]:
        raise ValueError(f"MCP returned different region or model for {case}")
    receipt = {"historical": stats, "records": data["record_count"],
               "daily_rows_in_reply": len(data["daily"]), "spilled": data["truncated"]}
    if staged:
        if not data.get("truncated") or not data.get("canvas_id") or not data.get("table_name"):
            raise ValueError(f"Expected hourly + daily request to spill for {case}")
        result, sql_stats = client.call(QUERY, {"canvas_id": data["canvas_id"],
                                        "sql": longest_spell_sql(data["table_name"], inputs["threshold"])})
        rows = result["structuredContent"]
        if rows["truncated"] or rows["row_count"] != 1 or len(rows["rows"]) != 1:
            raise ValueError(f"SQL aggregation expected one row for {case}")
        receipt.update({"hourly_rows_in_reply": len(data["hourly"]), "sql": sql_stats})
        answer = rows["rows"][0]
    else:
        if data["truncated"] or data["record_count"] != len(data["daily"]):
            raise ValueError(f"MCP daily result incomplete for {case}")
        daily = {key: [row[key] for row in data["daily"]] for key in ("time", "temperature_2m_max")}
        frozen = fetch(case)
        if daily != frozen["daily"] or data["daily_units"] != frozen["daily_units"]:
            raise ValueError(f"MCP daily data differs from fixture for {case}")
        answer = native({"daily": daily, "daily_units": data["daily_units"]}, inputs["threshold"])
    if answer != expected(case):
        raise ValueError(f"Wrong MCP answer for {case}: {answer}")
    receipt["answer"] = answer
    return receipt


def main():
    client = WeatherMCP()
    client.initialize()
    report = {"observed_at": datetime.now(timezone.utc).isoformat(), "endpoint": URL,
              "server": client.info["name"], "version": client.info["version"], "cases": {}}
    for index, case in enumerate(CASES):
        rows = {}
        for staged in ([False, True] if index % 2 == 0 else [True, False]):
            rows["hourly_plus_daily_sql" if staged else "daily_inline"] = check(case, client, staged)
        report["cases"][case] = rows
    print(json.dumps(report, indent=2, allow_nan=False))


if __name__ == "__main__":
    main()
