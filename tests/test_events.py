"""Dated event table: the ISS SSTV series override must be found inside
its window and only there, case-insensitively."""
from datetime import datetime, timezone

from sdrstream import events


def _at(ts):
    return datetime.fromisoformat(ts.replace("Z", "+00:00"))


def test_series34_active_during_window():
    ev = events.active_event("ISS", at=_at("2026-10-23T12:00:00Z"))
    assert ev is not None
    assert ev.downlink_hz == 437_550_000
    assert ev.image_mode == "Robot36"


def test_series34_not_outside_window():
    assert events.active_event("ISS", at=_at("2026-10-27T00:00:00Z")) is None
    assert events.active_event("ISS", at=_at("2026-10-22T15:00:00Z")) is None


def test_no_event_for_decommissioned_birds():
    assert events.active_event("NOAA 15", at=_at("2026-10-23T12:00:00Z")) is None


def test_lookup_case_insensitive():
    assert events.active_event("iss", at=_at("2026-10-23T12:00:00Z")) is not None


def test_event_override_feeds_plan_frequency():
    # The plan builder must tune the event downlink, not the legacy
    # 145.800 catalogue entry, while the event is active.
    from sdrstream import satellites

    iss = satellites.SATELLITES["ISS"]
    assert iss.downlink_hz == 145_800_000  # legacy catalogue entry stays
    ev = events.active_event(iss.name, at=_at("2026-10-23T12:00:00Z"))
    effective = ev.downlink_hz if ev else iss.downlink_hz
    assert effective == 437_550_000
