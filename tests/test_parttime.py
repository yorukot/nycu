#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.11"
# dependencies = ["typer", "rich", "playwright>=1.57"]
# ///
"""Offline self-check for scripts/parttime.py's parsing/selection logic.

Runs with no network and no portal login: loads parttime.py as a module and
exercises its pure functions against the synthetic attendance fixture
(an 8-row attendance grid with the same ASP.NET structure) plus a synthetic two-button row. Same dependencies as
parttime.py itself so `uv run` can import it directly.

    ./tests/test_parttime.py
"""
from __future__ import annotations

import importlib.util
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent


spec = importlib.util.spec_from_file_location("parttime", ROOT / "scripts" / "parttime.py")
parttime = importlib.util.module_from_spec(spec)
sys.modules["parttime"] = parttime
spec.loader.exec_module(parttime)  # type: ignore[union-attr]

failures: list[str] = []
total = 0


def check(name: str, condition: bool) -> None:
    global total
    total += 1
    print(f"[{'ok' if condition else 'FAIL'}] {name}")
    if not condition:
        failures.append(name)


# ── synthetic fixture: 8 rows, only row 0 has sign-in ────────────────
def fixture_row(index):
    button = '<a id="x_LinkButton_signIn_0">SignIn</a>' if index == 0 else ""
    return f'<tr><td>Example</td><td>TEST{index}　Example Project　Example Person(TEST)(, )　2026-01-01~2026-01-31</td><td>{button}</td></tr>'

real_html = '<table id="ContentPlaceHolder1_GridView_attend">' + ''.join(fixture_row(i) for i in range(8)) + '</table>'
rows = parttime._parse_grid(real_html)
check("fixture: 8 rows parsed", len(rows) == 8)
check("fixture: row 0 can_sign_in, not can_sign_out", rows[0]["can_sign_in"] is True and rows[0]["can_sign_out"] is False)
check("fixture: rows 1-7 have neither button", all(not r["can_sign_in"] and not r["can_sign_out"] for r in rows[1:]))

# ── synthetic row with BOTH buttons: correct target per action ──
SYNTHETIC_ROW = """
<tr><td align="center">Test</td><td align="left">
<p> <a href='#'>TEST001　Test Project　王小明(AB1234)(, )　2026-01-01~2026-01-31</a></p>
<p> 每月工時上限： 目前累積：2小時 尚缺：</p>
</td><td>
<a id="x_LinkButton_signIn_0" href="javascript:WebForm_DoPostBackWithOptions(new WebForm_PostBackOptions(&quot;ctl00$ContentPlaceHolder1$GridView_attend$ctl02$LinkButton_signIn&quot;, &quot;&quot;, true, &quot;&quot;, &quot;&quot;, false, true))">SignIn</a>
<a id="x_LinkButton_signOut_0" href="javascript:WebForm_DoPostBackWithOptions(new WebForm_PostBackOptions(&quot;ctl00$ContentPlaceHolder1$GridView_attend$ctl02$LinkButton_signOut&quot;, &quot;&quot;, true, &quot;&quot;, &quot;&quot;, false, true))">SignOut</a>
</td></tr>
"""
synthetic_table = f'<table id="ContentPlaceHolder1_GridView_attend">{SYNTHETIC_ROW}</table>'
synthetic_rows = parttime._parse_grid(synthetic_table)
check("synthetic: both buttons detected", synthetic_rows[0]["can_sign_in"] and synthetic_rows[0]["can_sign_out"])

sign_in_target = parttime._find_postback_target(SYNTHETIC_ROW, "can_sign_in")
sign_out_target = parttime._find_postback_target(SYNTHETIC_ROW, "can_sign_out")
check("sign-in picks a LinkButton_signIn target", bool(sign_in_target) and sign_in_target.endswith("LinkButton_signIn"))
check("sign-out picks a LinkButton_signOut target", bool(sign_out_target) and sign_out_target.endswith("LinkButton_signOut"))
check("the two targets differ (the original wrong-write bug)", sign_in_target != sign_out_target)

# ── row with only sign-in present: sign-out lookup must miss, not misfire ──
sign_in_only_html = SYNTHETIC_ROW.split('<a id="x_LinkButton_signOut_0"')[0]
missing_target = parttime._find_postback_target(sign_in_only_html, "can_sign_out")
check("sign-in-only row: sign-out target is None (fails before any POST)", missing_target is None)

# ── --row validation ─────────────────────────────────────────────
try:
    parttime._pick_row(rows, 99, "can_sign_in")
    check("row 99 (out of range) raises ParttimeError", False)
except parttime.ParttimeError:
    check("row 99 (out of range) raises ParttimeError", True)

try:
    parttime._pick_row(rows, -1, "can_sign_in")
    check("row -1 (negative) raises ParttimeError", False)
except parttime.ParttimeError:
    check("row -1 (negative) raises ParttimeError", True)

try:
    parttime._pick_row(rows, 1, "can_sign_in")  # row 1 has neither button
    check("row 1 (action unavailable) raises ParttimeError", False)
except parttime.ParttimeError:
    check("row 1 (action unavailable) raises ParttimeError", True)

check("row 0 (action available) returns cleanly", parttime._pick_row(rows, 0, "can_sign_in") == 0)

# ── commit verification (item 3): row state must actually flip ──
before = {"can_sign_in": True, "can_sign_out": False}
after_signed_in = {"can_sign_in": False, "can_sign_out": True}
after_unchanged = {"can_sign_in": True, "can_sign_out": False}
check("flip detected when sign-in actually commits", parttime._flipped("can_sign_in", before, after_signed_in))
check("no flip detected when nothing changed", not parttime._flipped("can_sign_in", before, after_unchanged))
check("missing refreshed row never counts as a flip", not parttime._flipped("can_sign_in", before, None))

# ── hidden-field extraction only takes type="hidden" (item 7) ───
mixed_inputs = """
<input type="hidden" name="__VIEWSTATE" value="abc" />
<input type="checkbox" name="reason_transfer" value="on" />
<input name="__EVENTTARGET" type="hidden" value="" />
<input type="submit" name="ctl00$ContentPlaceHolder1$Button_attend" value="確認" />
"""
hidden = parttime._hidden_fields(mixed_inputs)
check("hidden field extraction keeps __VIEWSTATE", hidden.get("__VIEWSTATE") == "abc")
check("hidden field extraction keeps __EVENTTARGET regardless of attribute order", "__EVENTTARGET" in hidden)
check("hidden field extraction excludes the checkbox", "reason_transfer" not in hidden)
check("hidden field extraction excludes the visible submit button", "ctl00$ContentPlaceHolder1$Button_attend" not in hidden)

if failures:
    print(f"\n{len(failures)}/{total} check(s) failed: {failures}")
    sys.exit(1)
print(f"\nall {total} checks passed")
