#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.11"
# dependencies = ["typer", "rich"]
# ///
"""NYCU timetable atoms: semesters / search / lookup / periods.

Wraps timetable.nycu.edu.tw's public course query API (no auth required) so
agents can answer "what day/period/room is this class", something E3/Moodle
does not carry. cos_id is the same identifier E3 uses in its shortname
(e.g. `1151.535702`: the part before the dot is the acysem, the part after is
the cos_id), so `lookup` chains directly off `e3p courses` output.
"""
from __future__ import annotations

import sys as _sys
from pathlib import Path as _Path

# Siblings shadow stdlib (json.py). Drop our dir off sys.path so typer/rich
# and urllib resolve the stdlib versions normally.
_sys.path[:] = [p for p in _sys.path if _Path(p).resolve() != _Path(__file__).resolve().parent]

# Add ../lib for shared output helpers (envelope, fail).
_LIB = str(_Path(__file__).resolve().parent.parent / "lib")
if _LIB not in _sys.path:
    _sys.path.insert(0, _LIB)

import html
import json
import re
from concurrent.futures import ThreadPoolExecutor
from enum import Enum
from typing import Any, Optional
from urllib.error import HTTPError, URLError
from urllib.parse import urlencode
from urllib.request import Request, urlopen

import ssl

# timetable.nycu.edu.tw's cert chain lacks the Subject Key Identifier extension,
# which Python 3.13+ rejects under VERIFY_X509_STRICT. Keep full verification,
# drop only the strict-extension check (same fix as e3p.py).
_SSL_CTX = ssl.create_default_context()
_SSL_CTX.verify_flags &= ~ssl.VERIFY_X509_STRICT

import typer
from rich.console import Console
from rich.table import Table

from _envelope import emit, fail  # noqa: E402

BASE = "https://timetable.nycu.edu.tw/"

app = typer.Typer(
    rich_markup_mode=None,
    no_args_is_help=True,
    add_completion=False,
    help="NYCU timetable atoms: semesters / search / lookup / periods.",
)
console = Console()

DAY_NAMES = {"M": "Mon", "T": "Tue", "W": "Wed", "R": "Thu", "F": "Fri", "S": "Sat", "U": "Sun"}

PERIODS = [
    ("y", "06:00", "06:50"),
    ("z", "07:00", "07:50"),
    ("1", "08:00", "08:50"),
    ("2", "09:00", "09:50"),
    ("3", "10:10", "11:00"),
    ("4", "11:10", "12:00"),
    ("n", "12:20", "13:10"),
    ("5", "13:20", "14:10"),
    ("6", "14:20", "15:10"),
    ("7", "15:30", "16:20"),
    ("8", "16:30", "17:20"),
    ("9", "17:30", "18:20"),
    ("a", "18:30", "19:20"),
    ("b", "19:30", "20:20"),
    ("c", "20:30", "21:20"),
    ("d", "21:30", "22:20"),
]
PERIOD_MAP = {code: (start, end) for code, start, end in PERIODS}

_SKIP_KEYS = {"dep_id", "dep_cname", "dep_ename", "costype", "brief", "language"}

# One <day><periods> run, e.g. "M78" or "R34". Periods only use the real
# period-code alphabet so a stray letter (e.g. the B/A in "TBA") never
# matches and gets mistaken for a day.
_DAY_RUN = re.compile(r"([MTWRFSU])([yz1-9nabcd]+)")
_DAY_RUN_SEQUENCE = re.compile(r"(?:[MTWRFSU][yz1-9nabcd]+)+")
_ROOM_RE = re.compile(r"^([^\[]*)(?:\[([^\]]*)\])?$")


class TimetableError(Exception):
    """Raised by HTTP/parsing helpers instead of calling fail() directly.

    Worker threads (see `lookup`) must never call fail(): sys.exit() only
    kills that thread, so every failing worker would print its own envelope.
    Helpers raise this instead, and exactly one call site (the main thread,
    after a ThreadPoolExecutor drains) converts it to a single fail().
    """

    def __init__(self, message: str, why: Optional[str] = None, hint: Optional[str] = None) -> None:
        super().__init__(message)
        self.message = message
        self.why = why
        self.hint = hint


def _fail_from(e: TimetableError) -> None:
    fail(e.message, why=e.why, hint=e.hint)


class SearchBy(str, Enum):
    name = "name"
    teacher = "teacher"
    code = "code"


_BY_OPTION = {SearchBy.name: "crsname", SearchBy.teacher: "teaname", SearchBy.code: "cos_code"}


# ── HTTP plumbing ────────────────────────────────────────────────
def _call(r: str, data: Optional[dict] = None) -> Any:
    url = f"{BASE}?r={r}"
    headers = {"User-Agent": "utils-timetable/1.0"}
    try:
        if data is None:
            req = Request(url, headers=headers)
        else:
            req = Request(url, data=urlencode(data).encode(), headers=headers)
        with urlopen(req, timeout=30, context=_SSL_CTX) as resp:
            raw = resp.read()
    except HTTPError as e:
        if 400 <= e.code < 500:
            raise TimetableError(
                "timetable rejected the query",
                why=f"{e.code} {e.reason}",
                hint="check --acysem against `timetable semesters` and the search value",
            ) from e
        raise TimetableError(
            "timetable unreachable", why=f"{e.code} {e.reason}", hint="check network or https://timetable.nycu.edu.tw"
        ) from e
    except URLError as e:
        raise TimetableError("timetable unreachable", why=str(e), hint="check network or https://timetable.nycu.edu.tw") from e
    try:
        return json.loads(raw)
    except json.JSONDecodeError:
        raise TimetableError(
            "timetable: non-JSON response",
            why=raw[:200].decode(errors="replace"),
            hint="check the acysem / search value; the API returns plain text on a malformed query",
        ) from None


def _semesters() -> list[str]:
    entries = _call("main/get_acysem")
    return [e["T"] for e in entries]


def _default_acysem() -> str:
    sems = _semesters()
    if not sems:
        raise TimetableError("no semesters available", why="get_acysem returned an empty list", hint="pass --acysem explicitly")
    return sems[0]


def _split_acysem(acysem: str) -> tuple[str, str]:
    if len(acysem) not in (3, 4):
        raise TimetableError("bad acysem", why=f"{acysem!r} is not 3 or 4 characters", hint="use a code from `timetable semesters`, e.g. 1151 or 99X")
    acy, sem = acysem[:-1], acysem[-1]
    if sem not in ("1", "2", "X"):
        raise TimetableError(
            "bad acysem", why=f"{acysem!r} has semester {sem!r}, expected 1, 2, or X", hint="use a code from `timetable semesters`, e.g. 1151 or 99X"
        )
    return acy, sem


def _query(acysem: str, option: str, value: str) -> Any:
    acy, sem = _split_acysem(acysem)
    fields = {
        "m_acy": acy,
        "m_sem": sem,
        "m_acyend": acy,
        "m_semend": sem,
        "m_dep_uid": "**",
        "m_group": "**",
        "m_grade": "**",
        "m_class": "**",
        "m_option": option,
        "m_crsname": "**",
        "m_teaname": "**",
        "m_cos_id": "**",
        "m_cos_code": "**",
        "m_crstime": "**",
        "m_crsoutline": "**",
        "m_costype": "**",
        "m_selcampus": "**",
    }
    key = {"crsname": "m_crsname", "teaname": "m_teaname", "cos_id": "m_cos_id", "cos_code": "m_cos_code"}[option]
    fields[key] = value
    result = _call("main/get_cos_list", fields)
    if isinstance(result, str):
        raise TimetableError("timetable rejected the query", why=result, hint="check the acysem or search value")
    return result


# ── parsing helpers ──────────────────────────────────────────────
def _strip_html(s: str) -> str:
    s = re.sub(r"<[^>]+>", "", s or "")
    return html.unescape(s).strip()


def _to_float(v: Any) -> Optional[float]:
    try:
        return float(v)
    except (TypeError, ValueError):
        return None


def _parse_head_runs(head: str) -> list[tuple[Optional[str], str]]:
    """Split the part of a chunk before the room dash into (day, periods) runs.

    A chunk can pack more than one day into one room, e.g. "M78R34" is Mon
    7-8 AND Thu 3-4. Only decompose when the whole head is nothing but valid
    day+period runs; anything else (an unknown day letter, "TBA", ...) is
    kept as one raw, undated run rather than guessing.
    """
    if head and _DAY_RUN_SEQUENCE.fullmatch(head):
        return _DAY_RUN.findall(head)
    return [(None, head)]


def _parse_cos_time(raw: str) -> list[dict]:
    slots: list[dict] = []
    for chunk in (raw or "").split(","):
        chunk = chunk.strip()
        if not chunk:
            continue
        head, _sep, rest = chunk.partition("-")
        room_match = _ROOM_RE.match(rest)
        room = (room_match.group(1).strip() or None) if room_match else None
        campus = (room_match.group(2) or None) if room_match else None
        for day, period_codes in _parse_head_runs(head):
            day_name = DAY_NAMES.get(day) if day else None
            if day_name is None:
                start = end = None
            else:
                start = PERIOD_MAP.get(period_codes[0], (None, None))[0]
                end = PERIOD_MAP.get(period_codes[-1], (None, None))[1]
            slots.append(
                {
                    "day": day_name,
                    "periods": period_codes,
                    "start": start,
                    "end": end,
                    "room": room,
                    "campus": campus,
                }
            )
    return slots


def _parse_courses(raw: Any) -> list[dict]:
    if not isinstance(raw, dict):
        return []
    courses: dict[str, dict] = {}
    for dep in raw.values():
        if not isinstance(dep, dict):
            continue
        dep_cname = dep.get("dep_cname")
        for group_key, group in dep.items():
            if group_key in _SKIP_KEYS or not isinstance(group, dict):
                continue
            for cos_key, course in group.items():
                if cos_key not in courses:
                    limit_raw = course.get("num_limit")
                    limit = int(limit_raw) if isinstance(limit_raw, str) and limit_raw.isdigit() else None
                    cos_time = course.get("cos_time") or ""
                    courses[cos_key] = {
                        "acysem": f"{course.get('acy', '')}{course.get('sem', '')}",
                        "cos_id": course.get("cos_id"),
                        "cos_code": course.get("cos_code") or None,
                        "name": course.get("cos_cname"),
                        "name_en": course.get("cos_ename") or None,
                        "teacher": course.get("teacher") or None,
                        "credit": _to_float(course.get("cos_credit")),
                        "hours": _to_float(course.get("cos_hours")),
                        "type": course.get("cos_type") or None,
                        "time": cos_time,
                        "slots": _parse_cos_time(cos_time),
                        "departments": [],
                        "limit": limit,
                        "memo": _strip_html(course.get("memo") or ""),
                    }
                entry_dep = dep_cname or course.get("dep_cname")
                if entry_dep and entry_dep not in courses[cos_key]["departments"]:
                    courses[cos_key]["departments"].append(entry_dep)
    return list(courses.values())


def _human_courses(data: list[dict], metadata: dict) -> None:
    t = Table(show_header=True, header_style="bold")
    t.add_column("cos_id")
    t.add_column("Name")
    t.add_column("Teacher")
    t.add_column("Time")
    for c in data:
        t.add_row(str(c["cos_id"]), c["name"] or "-", c["teacher"] or "-", c["time"] or "-")
    console.print(t)
    if metadata.get("missing"):
        console.print(f"[yellow]missing:[/] {', '.join(metadata['missing'])}")


# ── commands ─────────────────────────────────────────────────────
@app.command(help="List semester codes the timetable system knows about, newest first.")
def semesters() -> None:
    try:
        sems = _semesters()
    except TimetableError as e:
        _fail_from(e)
        return
    emit(sems, {"count": len(sems)}, human=lambda d, _m: console.print(", ".join(d)))


@app.command(help="Search courses by name, teacher, or course code.")
def search(
    query: str = typer.Argument(..., help="Search text (Chinese course name, teacher name, or course code)."),
    by: SearchBy = typer.Option(SearchBy.name, "--by", help="Field to search: name, teacher, or code."),
    acysem: Optional[str] = typer.Option(None, "--acysem", help="Semester code like 1151. Default: latest semester."),
) -> None:
    try:
        sem = acysem or _default_acysem()
        raw = _query(sem, _BY_OPTION[by], query)
    except TimetableError as e:
        _fail_from(e)
        return
    courses = _parse_courses(raw)
    emit(courses, {"count": len(courses), "acysem": sem, "query": query, "by": by.value}, human=_human_courses)


MAX_LOOKUP_IDS = 20


@app.command(help="Look up specific courses by cos_id (max 20 per call). IDs not found this semester are reported in metadata, not treated as an error.")
def lookup(
    cos_ids: list[str] = typer.Argument(..., help="One or more course IDs (cos_id), e.g. 535702. Max 20 per call."),
    acysem: Optional[str] = typer.Option(None, "--acysem", help="Semester code like 1151. Default: latest semester."),
) -> None:
    if len(cos_ids) > MAX_LOOKUP_IDS:
        fail(
            f"too many cos_ids ({len(cos_ids)})",
            why=f"lookup accepts at most {MAX_LOOKUP_IDS} ids per call",
            hint="split the ids across multiple `timetable lookup` calls",
        )
    try:
        sem = acysem or _default_acysem()
    except TimetableError as e:
        _fail_from(e)
        return

    def _lookup_one(cid: str) -> tuple[str, Optional[list[dict]], Optional[TimetableError]]:
        # Runs on a worker thread: never call fail() here (sys.exit only
        # kills this thread, so N failing ids would print N envelopes).
        # Errors are carried back to the main thread and raised once there.
        try:
            raw = _query(sem, "cos_id", cid)
        except TimetableError as e:
            return cid, None, e
        except ValueError as e:
            return cid, None, TimetableError("timetable lookup failed", why=str(e), hint="check the cos_id value")
        return cid, _parse_courses(raw), None

    ids = [str(cid) for cid in cos_ids]
    with ThreadPoolExecutor(max_workers=4) as pool:
        results = list(pool.map(_lookup_one, ids))

    first_error = next((err for _, _, err in results if err is not None), None)
    if first_error is not None:
        _fail_from(first_error)
        return

    found: dict[str, dict] = {}
    missing: list[str] = []
    for cid, courses, _err in results:
        if not courses:
            missing.append(cid)
            continue
        for c in courses:
            found[c["cos_id"]] = c
    rows = list(found.values())
    emit(rows, {"count": len(rows), "acysem": sem, "missing": missing}, human=_human_courses)


@app.command(help="Static reference table: period code -> start/end time, and day-letter -> weekday name.")
def periods() -> None:
    data = {
        "periods": [{"code": c, "start": s, "end": e} for c, s, e in PERIODS],
        "days": DAY_NAMES,
    }

    def human(d: dict, _m: dict) -> None:
        t = Table(show_header=True, header_style="bold")
        t.add_column("Code")
        t.add_column("Start")
        t.add_column("End")
        for p in d["periods"]:
            t.add_row(p["code"], p["start"], p["end"])
        console.print(t)
        console.print(f"days: {d['days']}")

    emit(data, human=human)


if __name__ == "__main__":
    app()
