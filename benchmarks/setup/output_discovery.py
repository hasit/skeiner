"""Check successful response schemas by inspecting ordinary MCP discovery before any read."""
import json
import secrets
import tempfile
from pathlib import Path

if __package__:
    from .acceptance import Mcp, key
    from .fixtures.connect import origin
    from .spec import verify
else:
    from acceptance import Mcp, key
    from fixtures.connect import origin
    from spec import verify

FIELDS = {"id": "string", "name": "string", "description": "string", "type": "string",
          "createdAt": "string", "updatedAt": "string", "items": "integer",
          "attributeVersion": "integer", "contentVersion": "integer"}
ENUM = {"USER_CREATED", "PERSONAL", "EVERYONE", "TRANSFER"}


def documents(text):
    """Read JSON schemas embedded in an operation description, including fenced JSON."""
    decoder = json.JSONDecoder()
    pos = 0
    while (pos := text.find("{", pos)) >= 0:
        try:
            value, end = decoder.raw_decode(text, pos)
        except ValueError:
            pos += 1
            continue
        yield value
        pos = end


def schema_from_metadata(node):
    """Unwrap a JSON schema, OpenAPI 200 response or a namespaced response annotation."""
    if not isinstance(node, dict):
        return
    if any(k in node for k in ("type", "$ref", "properties")):
        yield node
    for field, child in node.items():
        name = field.lower().replace("-", "").replace("_", "").split("/")[-1]
        if name in {"outputschema", "responseschema", "response", "responses", "schema", "200",
                    "content", "json", "xresponseschema", "xoutputschema", "meta", "annotations"}:
            yield from schema_from_metadata(child)


def candidates(tool):
    for container in (tool, tool.get("inputSchema", {})):
        for field, value in container.items():
            if field in ("_meta", "annotations") or "output" in field.lower() or "response" in field.lower():
                for schema in schema_from_metadata(value):
                    yield schema, (schema, value, container, tool)
        for document in documents(container.get("description", "")):
            for schema in schema_from_metadata(document):
                yield schema, (schema, document, container, tool)


def resolve(schema, roots):
    """Resolve only local JSON pointers using metadata available to the client."""
    seen = set()
    while isinstance(schema, dict) and "$ref" in schema:
        ref = schema["$ref"]
        if not isinstance(ref, str) or not ref.startswith("#/") or ref in seen:
            return {}
        seen.add(ref)
        for root in roots:
            target = root
            for segment in ref[2:].split("/"):
                target = target.get(segment.replace("~1", "/").replace("~0", "~")) if isinstance(target, dict) else None
            if isinstance(target, dict):
                schema = target
                break
        else:
            return {}
    return schema if isinstance(schema, dict) else {}


def inspect(tool, is_list, extra):
    blank = {"shape": False, "nine_field_types": False, "four_type_enum_values": False}
    if extra:
        blank["new_optional_boolean"] = False
    best = blank
    for schema, roots in candidates(tool):
        schema = resolve(schema, roots)
        props = schema.get("properties", {})
        # MCP outputSchema requires an object, so accept a single-field payload wrapper.
        if schema.get("type") == "object" and isinstance(props, dict) and len(props) == 1:
            schema = resolve(next(iter(props.values())), roots)
        if is_list:
            if schema.get("type") != "array":
                continue
            schema = resolve(schema.get("items"), roots)
        if schema.get("type") != "object" or not isinstance(schema.get("properties", {}), dict):
            continue
        properties = {name: resolve(node, roots) for name, node in schema.get("properties", {}).items()}
        kinds = properties.get("type", {}).get("enum", [])
        result = {"shape": True,
                  "nine_field_types": all(properties.get(k, {}).get("type") == v for k, v in FIELDS.items()),
                  "four_type_enum_values": (isinstance(kinds, list) and len(kinds) == 4
                                           and all(isinstance(k, str) for k in kinds) and set(kinds) == ENUM)}
        if extra:
            result["new_optional_boolean"] = (properties.get(extra, {}).get("type") == "boolean"
                                               and extra not in schema.get("required", []))
        if sum(result.values()) > sum(best.values()):
            best = result
    return best


def epoch(launch, spec, extra):
    with origin() as (data, base):
        error = None
        ops = {}
        try:
            with launch(base, data.token, spec) as proc:
                client = Mcp(proc, data.token)
                tools = client.connect()
                if {t["name"] for t in tools} == {"search", "execute"}:
                    ops = {key(h["operationId"]): h for h in client.call("search", {"query": "vault"})}
                else:
                    ops = {key(t["name"]): t for t in tools}
        except Exception as exc:
            error = type(exc).__name__
        checks = {"no_upstream_requests": not data.trace}
        for operation in ("GetVaults", "GetVaultById"):
            values = inspect(ops.get(key(operation), {}), operation == "GetVaults", extra)
            checks.update({operation + "." + label: value for label, value in values.items()})
        report = {"phase": "modified_response" if extra else "upstream", "checks": checks}
        if error:
            report["error"] = error
        return report


def check(launch, spec_path):
    """Grade response discovery independently. Run this after the primary setup check passes."""
    spec = Path(spec_path).resolve()
    verify(spec)
    extra = "metadata" + secrets.token_hex(6)
    marker = "    Vault:\n      type: object\n      properties:\n"
    original = spec.read_text()
    if original.count(marker) != 1:
        raise ValueError("expected the pinned Vault schema")
    with tempfile.TemporaryDirectory() as scratch:
        changed = Path(scratch) / "changed-response.yaml"
        changed.write_text(original.replace(marker, marker + f"        {extra}:\n          type: boolean\n"))
        epochs = [epoch(launch, spec, None), epoch(launch, changed, extra)]
    checks = [ok for entry in epochs for ok in entry["checks"].values()]
    return {"passed": all(checks) and not any("error" in e for e in epochs), "checks_total": len(checks),
            "checks_passed": sum(checks), "epochs": epochs}
