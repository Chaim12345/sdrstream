"""Tests for the pass-recording scheduler (fake planner and recorder)."""

from __future__ import annotations

import tempfile
import unittest
from datetime import datetime, timedelta, timezone
from pathlib import Path

from sdrstream.schedule import Job, Scheduler, _pass_key


def _plan(hours_ahead: float = 0.05):
    """A fake pass plan that starts soon and carries a satellite name."""

    class _Plan:
        def __init__(self):
            self.start = datetime.now(timezone.utc) + timedelta(hours=hours_ahead)
            self.satellite = type("S", (), {"name": "ISS"})()

    return _Plan()


class SchedulerStoreTest(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.store = Path(self._tmp.name) / "schedule.json"

    def tearDown(self):
        self._tmp.cleanup()

    def test_add_remove_round_trip(self) -> None:
        scheduler = Scheduler(self.store)
        scheduler.add("ISS", output_dir="caps", min_elevation=15.0)
        self.assertEqual(len(scheduler.jobs()), 1)
        # Persisted: a fresh instance sees the same job.
        reloaded = Scheduler(self.store)
        self.assertEqual(len(reloaded.jobs()), 1)
        self.assertEqual(reloaded.jobs()[0].satellite, "ISS")
        self.assertEqual(reloaded.jobs()[0].min_elevation, 15.0)
        self.assertTrue(reloaded.remove("ISS"))
        self.assertEqual(Scheduler(self.store).jobs(), [])

    def test_corrupt_store_starts_fresh(self) -> None:
        self.store.parent.mkdir(parents=True, exist_ok=True)
        self.store.write_text("{not json")
        scheduler = Scheduler(self.store)
        self.assertEqual(scheduler.jobs(), [])

    def test_done_list_is_bounded_on_save(self) -> None:
        job = Job(satellite="ISS", done=["k%d" % i for i in range(300)])
        self.assertLessEqual(len(job.to_dict()["done"]), 100)


class SchedulerRunTest(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.store = Path(self._tmp.name) / "schedule.json"
        self.plans = []
        self.recorded = []

    def tearDown(self):
        self._tmp.cleanup()

    def _scheduler(self) -> Scheduler:
        # One stable plan: a real pass start does not move between checks.
        plan = _plan()

        def planner(satellite, **kwargs):
            self.plans.append(satellite.name)
            return plan

        async def recorder(plan_arg, out_path, on_progress=None):
            self.recorded.append((out_path, plan_arg))
            return 42

        return Scheduler(self.store, planner=planner, recorder=recorder)

    def test_run_once_records_a_due_pass_once(self) -> None:
        scheduler = self._scheduler()
        scheduler.add("ISS", output_dir=str(Path(self._tmp.name) / "caps"))
        first = scheduler.run_once(within_seconds=300.0)
        self.assertEqual(first, 1)
        self.assertEqual(len(self.recorded), 1)
        out_path = self.recorded[0][0]
        self.assertIn("ISS", out_path)
        self.assertTrue(out_path.endswith(".wav"))
        # A second sweep does not re-record the same pass.
        second = scheduler.run_once(within_seconds=300.0)
        self.assertEqual(second, 0, "the recorded pass must not repeat")
        self.assertEqual(len(self.recorded), 1)
        # And the dedupe survives a restart.
        reloaded = Scheduler(self.store, planner=lambda s, **k: _plan(),
                            recorder=self.recorded.append if False else None)
        self.assertEqual(reloaded.jobs()[0].done,
                         [scheduler.jobs()[0].done[0]])

    def test_far_future_pass_is_not_due(self) -> None:
        scheduler = self._scheduler()
        scheduler.add("ISS")
        # Override the planner to a pass hours away.

        def far_planner(satellite, **kwargs):
            class _Far:
                start = datetime.now(timezone.utc) + timedelta(hours=6)

                class satellite:
                    name = "ISS"
            return _Far()

        scheduler._planner = far_planner
        self.assertEqual(scheduler.run_once(within_seconds=300.0), 0)

    def test_planner_failure_is_skipped(self) -> None:
        scheduler = self._scheduler()

        def broken_planner(satellite, **kwargs):
            raise ValueError("no TLE")

        scheduler._planner = broken_planner
        scheduler.add("ISS")
        self.assertEqual(scheduler.run_once(within_seconds=300.0), 0)

    def test_pass_key_normalises(self) -> None:
        start = datetime(2026, 10, 21, 13, 38, 44, tzinfo=timezone.utc)
        self.assertEqual(_pass_key("ISS", start), _pass_key("iss", start))


if __name__ == "__main__":
    unittest.main()
