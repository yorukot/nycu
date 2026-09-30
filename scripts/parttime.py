#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.11"
# dependencies = ["typer", "rich", "playwright>=1.57"]
# ///
"""NYCU part-time attendance atoms — status / sign-in / sign-out.

Wraps the NYCU part-time timeclock (timeclock.nycu.edu.tw), reached through
the shared portal SSO layer in `lib/_nycu_portal.py` (same layer `nycu.py`
uses). Only reachable from a campus IP (140.113.x) or the lab tailnet exit
via pve.

`sign-in` / `sign-out` create real attendance records once the second
postback fires. Always try `--dry-run` first: it stops after the first
postback at the confirmation page and commits nothing.
"""
from __future__ import annotations

import sys as _sys
from pathlib import Path as _Path

# Siblings shadow stdlib (json.py, re via nothing but keep consistent).
_sys.path[:] = [p for p in _sys.path if _Path(p).resolve() != _Path(__file__).resolve().parent]

_LIB = str(_Path(__file__).resolve().parent.parent / "lib")
if _LIB not in _sys.path:
    _sys.path.insert(0, _LIB)

import html
import re
from typing import Optional
from urllib.parse import urlparse

import typer
from rich.console import Console
from rich.table import Table

from _envelope import emit, fail  # noqa: E402
from _nycu_portal import PortalError, opener_post, relay, scrub  # noqa: E402

app = typer.Typer(
    rich_markup_mode=None,
    no_args_is_help=True,
    add_completion=False,
    help="NYCU part-time attendance atoms — status / sign-in / sign-out.",
)
console = Console()

SYS_DIRECT = "timeclockParttime"
TIMECLOCK_HOST = "timeclock.nycu.edu.tw"

_TABLE_RE = re.compile(r'<table[^>]*id="ContentPlaceHolder1_GridView_attend"[^>]*>(.*?)</table>', re.DOTALL)
_ROW_RE = re.compile(r"<tr[^>]*>(.*?)</tr>", re.DOTALL)
_TD_RE = re.compile(r"<td[^>]*>(.*?)</td>", re.DOTALL)
_TAG_RE = re.compile(r"<[^>]+>")
_SCRIPT_STYLE_RE = re.compile(r"<(script|style)\b[^>]*>.*?</\1>", re.DOTALL | re.IGNORECASE)
_DETAIL_RE = re.compile(
    r"(?P<code>\S+)　(?P<name>.+?)　(?P<pi>[^　()]+)\([^)]*\)\(,\s*\)　"
    r"(?P<start>\d{4}-\d{2}-\d{2})~(?P<end>\d{4}-\d{2}-\d{2})"
)
_HOURS_RE = re.compile(r"目前累積[：:]\s*(\d+)\s*小時")
_CAP_RE = re.compile(r"每月工時上限[：:]\s*([^目]*?)\s*目前累積")
_POSTBACK_RE = re.compile(
    r"WebForm_(?:DoPostBackWithOptions\(new WebForm_PostBackOptions\(|__doPostBack\()"
    r"&quot;([^&]+?)&quot;"
)
_INPUT_TAG_RE = re.compile(r"<input\b[^>]*>", re.IGNORECASE)
_NAME_ATTR_RE = re.compile(r'name="([^"]*)"', re.IGNORECASE)
_TYPE_ATTR_RE = re.compile(r'type="([^"]*)"', re.IGNORECASE)
_VALUE_ATTR_RE = re.compile(r'value="([^"]*)"', re.IGNORECASE)
_CONFIRM_BUTTON_NAME = "ctl00$ContentPlaceHolder1$Button_attend"
_CONFIRM_BUTTON_TAG_RE = re.compile(
    rf'<input[^>]*name="{re.escape(_CONFIRM_BUTTON_NAME)}"[^>]*>',
    re.IGNORECASE,
)

# Which grid button a given action posts back, and what the resulting
# confirm page must say before we trust it enough to submit the commit.
_ACTION_BUTTON = {"can_sign_in": "LinkButton_signIn", "can_sign_out": "LinkButton_signOut"}
_ACTION_CONFIRM_MARKER = {"can_sign_in": "簽到", "can_sign_out": "簽退"}


class ParttimeError(Exception):
    def __init__(self, message: str, why: Optional[str] = None, hint: Optional[str] = None) -> None:
        super().__init__(message)
        self.message = message
        self.why = why
        self.hint = hint


def _fail_from(e) -> None:
    fail(e.message, why=e.why, hint=e.hint)


_WS_RE = re.compile(r"[ \t\r\n]+")


def _strip(s: str) -> str:
    s = _SCRIPT_STYLE_RE.sub("", s or "")
    text = html.unescape(_TAG_RE.sub(" ", s))
    return _WS_RE.sub(" ", text).strip()


def _parse_grid(landing_html: str) -> list[dict]:
    table_match = _TABLE_RE.search(landing_html)
    if not table_match:
        raise ParttimeError(
            "timeclock attendance table not found",
            why="no #ContentPlaceHolder1_GridView_attend table in the landing page",
            hint="the timeclock page layout may have changed, or the relay landed somewhere unexpected",
        )
    rows: list[dict] = []
    for i, raw_row in enumerate(_ROW_RE.findall(table_match.group(1))):
        tds = _TD_RE.findall(raw_row)
        project_name = _strip(tds[0]) if len(tds) > 0 else None
        detail_html = tds[1] if len(tds) > 1 else ""
        detail_text = _strip(detail_html)
        actions_html = tds[2] if len(tds) > 2 else ""

        dm = _DETAIL_RE.search(detail_text)
        hm = _HOURS_RE.search(detail_text)
        cm = _CAP_RE.search(detail_text)

        sign_in_match = re.search(rf"LinkButton_signIn_{i}\b", actions_html)
        sign_out_match = re.search(rf"LinkButton_signOut_{i}\b", actions_html)

        rows.append({
            "row": i,
            "project_code": dm.group("code") if dm else None,
            "project_name": (dm.group("name") if dm else None) or project_name,
            "pi": dm.group("pi") if dm else None,
            "period_start": dm.group("start") if dm else None,
            "period_end": dm.group("end") if dm else None,
            "hours_accumulated": int(hm.group(1)) if hm else None,
            "hours_cap": (cm.group(1).strip() or None) if cm else None,
            "can_sign_in": bool(sign_in_match),
            "can_sign_out": bool(sign_out_match),
        })
    return rows


def _relay_status() -> tuple[object, str, str]:
    try:
        opener, landing_html, landing_url = relay(SYS_DIRECT)
    except PortalError as e:
        raise ParttimeError(e.message, why=e.why, hint=e.hint) from e
    host = urlparse(landing_url).hostname or ""
    if host != TIMECLOCK_HOST:
        raise ParttimeError(
            "relay did not land on the timeclock host",
            why=f"landed on {host or landing_url!r} instead of {TIMECLOCK_HOST}",
            hint="connect from campus network or tailnet exit via pve (140.113.x)",
        )
    return opener, landing_html, landing_url


def _hidden_fields(page_html: str) -> dict:
    """Collect name/value for every `<input type="hidden">` on the page.

    Attribute order in ASP.NET markup varies, so each `<input>` tag is
    matched whole and then probed for type/name/value independently rather
    than assuming a fixed order. Non-hidden inputs (radio/checkbox reason
    fields, the visible submit button) are deliberately excluded — they are
    added explicitly by the caller when needed.
    """
    fields: dict[str, str] = {}
    for tag in _INPUT_TAG_RE.findall(page_html):
        type_match = _TYPE_ATTR_RE.search(tag)
        if not type_match or type_match.group(1).lower() != "hidden":
            continue
        name_match = _NAME_ATTR_RE.search(tag)
        if not name_match:
            continue
        value_match = _VALUE_ATTR_RE.search(tag)
        fields[name_match.group(1)] = html.unescape(value_match.group(1)) if value_match else ""
    return fields


def _find_postback_target(row_html: str, action_key: str) -> Optional[str]:
    """Return the exact `__EVENTTARGET` for the row's sign-in/sign-out button.

    A row typically only has one action button, but does not guarantee it,
    so every postback href on the row is scanned and only the one whose
    target ends with the button name matching `action_key` is returned.
    Matching the first postback found (regardless of which button it is)
    was the original bug: a sign-out on a row with both buttons could POST
    the sign-in target instead.
    """
    suffix = _ACTION_BUTTON[action_key]
    for m in _POSTBACK_RE.finditer(row_html):
        target = m.group(1)
        if target.endswith(suffix):
            return target
    return None


def _flipped(action_key: str, before: dict, after: Optional[dict]) -> bool:
    """Did the row's button state actually change the way this action implies?

    sign-in: can_sign_in should go True->False, or can_sign_out should go
    False->True (whichever this timeclock instance uses to mark "done").
    sign-out is the mirror image. `after` being missing (row disappeared
    from the refreshed grid) never counts as a flip.
    """
    if after is None:
        return False
    if action_key == "can_sign_in":
        return (before["can_sign_in"] and not after["can_sign_in"]) or (not before["can_sign_out"] and after["can_sign_out"])
    return (before["can_sign_out"] and not after["can_sign_out"]) or (not before["can_sign_in"] and after["can_sign_in"])


@app.command(help="Show today's attendance grid: one row per project period, with sign-in/out availability.")
def status() -> None:
    try:
        _opener, landing_html, landing_url = _relay_status()
        rows = _parse_grid(landing_html)
    except ParttimeError as e:
        _fail_from(e)
        return

    def human(d: list[dict], _m: dict) -> None:
        t = Table(show_header=True, header_style="bold")
        t.add_column("Row")
        t.add_column("Project")
        t.add_column("Period")
        t.add_column("Hours")
        t.add_column("In")
        t.add_column("Out")
        for r in d:
            t.add_row(
                str(r["row"]),
                (r["project_name"] or "-")[:30],
                f"{r['period_start']}~{r['period_end']}" if r["period_start"] else "-",
                str(r["hours_accumulated"]) if r["hours_accumulated"] is not None else "-",
                "yes" if r["can_sign_in"] else "-",
                "yes" if r["can_sign_out"] else "-",
            )
        console.print(t)

    emit(rows, {"landing_url": landing_url, "campus_ip_required": True}, human=human)


def _pick_row(rows: list[dict], row: Optional[int], action_key: str) -> int:
    if row is not None:
        if row < 0 or row >= len(rows):
            raise ParttimeError(
                f"row {row} is out of range",
                why=f"today's grid has {len(rows)} row(s), valid indices are 0..{len(rows) - 1}" if rows else "today's grid has no rows",
                hint="run `parttime status` to see valid row indices",
            )
        if not rows[row].get(action_key):
            raise ParttimeError(
                f"row {row} does not have {action_key} available",
                why=f"row {row}: can_sign_in={rows[row]['can_sign_in']} can_sign_out={rows[row]['can_sign_out']}",
                hint="run `parttime status` and pick a row where the action is available",
            )
        return row
    candidates = [r["row"] for r in rows if r.get(action_key)]
    if len(candidates) == 0:
        raise ParttimeError(
            f"no row has {action_key} available",
            why="none of today's attendance rows offer that action",
            hint="run `parttime status` and pass --row explicitly if you expect one to be available",
        )
    if len(candidates) > 1:
        raise ParttimeError(
            f"more than one row has {action_key} available",
            why=f"rows {candidates} all qualify",
            hint="pass --row to pick one explicitly",
        )
    return candidates[0]


def _do_action(action_key: str, event_prefix: str, row_opt: Optional[int], dry_run: bool, yes: bool) -> None:
    if not yes:
        fail("refusing to act without confirmation", hint="pass --yes to confirm; this creates a real attendance record unless --dry-run is also set")

    try:
        opener, landing_html, landing_url = _relay_status()
        rows = _parse_grid(landing_html)
        row = _pick_row(rows, row_opt, action_key)

        row_html = list(_ROW_RE.finditer(_TABLE_RE.search(landing_html).group(1)))[row].group(1)
        event_target = _find_postback_target(row_html, action_key)
        if not event_target:
            raise ParttimeError(
                f"row {row} has no {_ACTION_BUTTON[action_key]} postback target",
                why=f"expected a WebForm_DoPostBackWithOptions/__doPostBack href naming {_ACTION_BUTTON[action_key]} on this row",
                hint="run `parttime status` again; the row's available actions may have changed",
            )

        fields = _hidden_fields(landing_html)
        fields["__EVENTTARGET"] = event_target
        fields["__EVENTARGUMENT"] = ""
        confirm_html = opener_post(opener, landing_url, fields)
        confirm_text = _strip(confirm_html)[:1000]

        marker = _ACTION_CONFIRM_MARKER[action_key]
        if marker not in confirm_text:
            raise ParttimeError(
                f"confirmation page does not look like a {event_prefix} page",
                why=scrub(f"expected {marker!r} in the confirm page text; got: {confirm_text[:300]}"),
                hint="run `parttime status` again; the postback may have hit the wrong button",
            )
    except (PortalError, ParttimeError) as e:
        _fail_from(e)
        return

    if dry_run:
        emit(
            {"dry_run": True, "row": row, "confirm_text": confirm_text},
            {"landing_url": landing_url},
            human=lambda d, _m: console.print(f"[yellow]dry-run[/] row {d['row']}: {d['confirm_text'][:300]}"),
        )
        return

    try:
        button_tag_match = _CONFIRM_BUTTON_TAG_RE.search(confirm_html)
        if not button_tag_match:
            raise ParttimeError(
                "confirmation page had no submit button",
                why=f"expected {_CONFIRM_BUTTON_NAME} in the response",
                hint="check confirm_text via --dry-run first; the confirm flow may have changed",
            )
        value_match = _VALUE_ATTR_RE.search(button_tag_match.group(0))
        confirm_fields = _hidden_fields(confirm_html)
        confirm_fields[_CONFIRM_BUTTON_NAME] = value_match.group(1) if value_match else ""

        opener_post(opener, landing_url, confirm_fields)
        _opener2, refreshed_html, _url2 = relay(SYS_DIRECT)
        refreshed_rows = _parse_grid(refreshed_html)
    except (PortalError, ParttimeError) as e:
        _fail_from(e)
        return

    refreshed_row = next((r for r in refreshed_rows if r["row"] == row), None)
    confirmed = _flipped(action_key, rows[row], refreshed_row)
    out = {"confirmed": confirmed, "page_text_excerpt": confirm_text[:300], "row": refreshed_row}

    def human(d: dict, _m: dict) -> None:
        if d["confirmed"]:
            console.print(f"[green]✓[/] {event_prefix} confirmed for row {row}")
        else:
            console.print(f"[red]✗[/] {event_prefix} posted for row {row} but the row's button state did not flip")
        if d.get("row"):
            console.print(f"  hours_accumulated: {d['row'].get('hours_accumulated')}")

    emit(out, {"landing_url": landing_url}, human=human)
    if not confirmed:
        _sys.exit(1)


@app.command(name="sign-in", help="Sign in for a project period. Requires --yes. Use --dry-run to stop at the confirmation page without committing anything.")
def sign_in(
    row: Optional[int] = typer.Option(None, "--row", help="Row index from `parttime status`. Default: the single row with can_sign_in true."),
    dry_run: bool = typer.Option(False, "--dry-run", help="Stop after the first postback and return the confirm page's text; commits nothing."),
    yes: bool = typer.Option(False, "--yes", help="Required explicit confirmation. This creates a real attendance record unless --dry-run is also set."),
) -> None:
    _do_action("can_sign_in", "sign-in", row, dry_run, yes)


@app.command(name="sign-out", help="Sign out for a project period. Requires --yes. Use --dry-run to stop at the confirmation page without committing anything.")
def sign_out(
    row: Optional[int] = typer.Option(None, "--row", help="Row index from `parttime status`. Default: the single row with can_sign_out true."),
    dry_run: bool = typer.Option(False, "--dry-run", help="Stop after the first postback and return the confirm page's text; commits nothing."),
    yes: bool = typer.Option(False, "--yes", help="Required explicit confirmation. This creates a real attendance record unless --dry-run is also set."),
) -> None:
    _do_action("can_sign_out", "sign-out", row, dry_run, yes)


if __name__ == "__main__":
    app()
