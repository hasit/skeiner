"""Captured ERA5 summer queries, native client, and independent SQL answer key."""
import hashlib
import json
import math
import os
import sqlite3
import ssl
from contextlib import contextmanager
from datetime import date, timedelta
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from threading import Thread
from urllib.parse import parse_qsl, urlencode, urlsplit
from urllib.request import urlopen

API = "https://archive-api.open-meteo.com/v1/archive"
FIXTURES = Path(__file__).resolve().parent / "fixtures"


def case_spec(lat, lon, zone, start, end, threshold):
    return {
        "latitude": lat, "longitude": lon, "timezone": zone,
        "start_date": start, "end_date": end,
        "daily": "temperature_2m_max", "models": "era5", "temperature_unit": "celsius",
        "threshold": threshold,
    }


CITIES = {
    "phoenix": (33.4484, -112.0740, "America/Phoenix", 40),
    "paris": (48.8566, 2.3522, "Europe/Paris", 30),
    "london": (51.5074, -0.1278, "Europe/London", 35),
}
CASES = {
    name: case_spec(lat, lon, zone, "2020-06-01", "2020-08-31", threshold)
    for name, (lat, lon, zone, threshold) in CITIES.items()
}
EXTENSION = {
    "new_york": case_spec(40.7128, -74.0060, "America/New_York", "2019-06-01", "2019-08-31", 30),
    "chicago": case_spec(41.8781, -87.6298, "America/Chicago", "2019-06-01", "2019-08-31", 30),
    "madrid": case_spec(40.4168, -3.7038, "Europe/Madrid", "2019-06-01", "2019-08-31", 30),
    "berlin": case_spec(52.5200, 13.4050, "Europe/Berlin", "2019-06-01", "2019-08-31", 30),
    "tokyo": case_spec(35.6762, 139.6503, "Asia/Tokyo", "2019-06-01", "2019-08-31", 30),
    "seoul": case_spec(37.5665, 126.9780, "Asia/Seoul", "2019-06-01", "2019-08-31", 30),
    "sydney": case_spec(-33.8688, 151.2093, "Australia/Sydney", "2019-12-01", "2020-02-29", 30),
}
ALL_CASES = {**CASES, **EXTENSION}


def query(case):
    """The archive has no streak or threshold parameter."""
    return {key: value for key, value in ALL_CASES[case].items() if key != "threshold"}


def project(raw):
    """Return just the temperatures after checking implicit date offsets are lossless."""
    dates = raw["daily"]["time"]
    values = raw["daily"]["temperature_2m_max"]
    if raw["daily_units"] != {"time": "iso8601", "temperature_2m_max": "°C"}:
        raise ValueError("expected ISO dates and Celsius daily maxima")
    if not dates or len(dates) != len(values):
        raise ValueError("expected one temperature per date")
    first_day = date.fromisoformat(dates[0])
    for index, (day, value) in enumerate(zip(dates, values)):
        if day != (first_day + timedelta(days=index)).isoformat():
            raise ValueError("date series must be ordered and consecutive")
        if value is not None and (type(value) not in (int, float) or not math.isfinite(value)):
            raise ValueError("expected a numeric temperature or null")
    return values


def tls_context():
    path = os.environ.get("SSL_CERT_FILE") or ssl.get_default_verify_paths().cafile
    # The macOS Python.org build can lack a configured OpenSSL trust bundle.
    if not path and Path("/etc/ssl/cert.pem").is_file():
        path = "/etc/ssl/cert.pem"
    return ssl.create_default_context(cafile=path)


def fetch(case, live=False):
    """Read an integrity-checked fixture, or make one request to the public service."""
    if live:
        with urlopen(API + "?" + urlencode(query(case)), timeout=30, context=tls_context()) as response:
            raw = json.load(response)
    else:
        content = (FIXTURES / f"{case}.json").read_bytes()
        hashes = json.loads((FIXTURES / "sha256.json").read_text())
        if hashlib.sha256(content).hexdigest() != hashes[case]:
            raise ValueError(f"{case} fixture checksum mismatch")
        raw = json.loads(content)
    project(raw)
    dates = raw["daily"]["time"]
    inp = ALL_CASES[case]
    if (dates[0], dates[-1], raw["timezone"]) != (inp["start_date"], inp["end_date"], inp["timezone"]):
        raise ValueError(f"{case} service response does not cover the requested local dates")
    return raw


def native(raw, threshold):
    """Handwritten direct client reduction, scanning all days once."""
    dates = raw["daily"]["time"]
    best = {"start": None, "end": None, "days": 0, "peak_c": None}
    days = 0
    peak = None
    for i, (day, temp) in enumerate(zip(dates, project(raw))):
        if temp is None or temp < threshold:
            days = 0
            peak = None
            continue
        days += 1
        peak = temp if peak is None else max(peak, temp)
        if days > best["days"]:
            best = {"start": dates[i - days + 1], "end": day, "days": days, "peak_c": peak}
    return best


def expected(case):
    """Group qualifying dates by date minus row number, then rank the islands in SQL."""
    raw = fetch(case)
    db = sqlite3.connect(":memory:")
    try:
        db.execute("CREATE TABLE readings (day TEXT PRIMARY KEY, temp REAL)")
        db.executemany("INSERT INTO readings VALUES (?, ?)", zip(raw["daily"]["time"], project(raw)))
        row = db.execute("""
            WITH hot AS (
                SELECT day, temp, julianday(day) - row_number() OVER (ORDER BY day) AS island
                FROM readings WHERE temp >= ?
            )
            SELECT min(day), max(day), count(*), max(temp) FROM hot
            GROUP BY island ORDER BY count(*) DESC, min(day) ASC LIMIT 1
        """, (ALL_CASES[case]["threshold"],)).fetchone()
    finally:
        db.close()
    return dict(zip(("start", "end", "days", "peak_c"), row or (None, None, 0, None)))


@contextmanager
def endpoint(reads=None):
    """Expose the recorded GET queries on loopback, with an optional read log."""
    def match(params, case):
        fields = dict(params)
        fields.setdefault("temperature_unit", "celsius")  # Open-Meteo's default
        try:
            for coord in ("latitude", "longitude"):
                fields[coord] = float(fields[coord])
        except (ValueError, KeyError):
            return False
        return fields == query(case)

    class Handler(BaseHTTPRequestHandler):
        def do_GET(self):
            url = urlsplit(self.path)
            params = parse_qsl(url.query, keep_blank_values=True)
            case = None
            if url.path == "/v1/archive" and len(params) == len(dict(params)):
                case = next((name for name in ALL_CASES if match(params, name)), None)
            if reads is not None:
                reads.append({"case": case, "ok": case is not None})
            data = fetch(case) if case else {"error": True, "reason": "No recorded response for this query"}
            body = json.dumps(data, ensure_ascii=False, separators=(",", ":")).encode()
            self.send_response(200 if case else 400)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *args):
            pass

    with ThreadingHTTPServer(("127.0.0.1", 0), Handler) as server:
        thread = Thread(target=server.serve_forever, kwargs={"poll_interval": 0.02}, daemon=True)
        thread.start()
        try:
            yield f"http://127.0.0.1:{server.server_port}"
        finally:
            server.shutdown()
            thread.join()
