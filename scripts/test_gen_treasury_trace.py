#!/usr/bin/env python3
"""Offline dataset/oracle checks; these do not call the model or run a chain."""

import collections
import json
import unittest

import gen_treasury_trace as scenario


class TreasuryScenarioTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.records, cls.report = scenario.generate()

    def test_volume_and_expected_rejections(self):
        self.assertEqual(22000, self.report["valid_background_payments"])
        self.assertEqual(22007, self.report["valid_pay"])
        self.assertEqual(22132, self.report["expected_valid_trace_transactions"])
        self.assertEqual(22156, len(self.records))
        self.assertEqual(24, len(self.report["expected_failures"]))
        self.assertEqual(111, self.report["agent_count"])
        self.assertLess(self.report["last_scheduled_ms"], 480000)
        self.assertEqual(1, sum(r["agent_id"] == scenario.TREASURER for r in self.records))

    def test_deterministic_and_unique(self):
        again, report = scenario.generate()
        self.assertEqual(self.records, again)
        self.assertEqual(self.report, report)
        ids = [r["request_id"] for r in self.records]
        self.assertEqual(len(ids), len(set(ids)))
        self.assertEqual(self.records, json.loads(json.dumps(self.records)))

    def test_unexpected_invalid_payment_is_detected(self):
        records = [dict(r) for r in self.records]
        record = next(r for r in records if r["request_id"].startswith("background-"))
        record["target"] = "never-joined"
        with self.assertRaisesRegex(ValueError, "unexpected lifecycle"):
            scenario.validate(records)

    def test_reference_policy_can_complete_without_repeated_payments(self):
        # This is an oracle for feasibility, not the runtime decision source.
        # Apply the requested goal every 15 seconds with immediate settlements.
        active = set()
        paid = collections.Counter()
        balance = 15
        cursor = 0
        payments = []
        for now in range(15000, 600001, 15000):
            while cursor < len(self.records) and self.records[cursor]["ts"] <= now:
                record = self.records[cursor]
                aid, action = record["agent_id"], record["action"]
                if action == "join":
                    active.add(aid)
                elif action == "leave":
                    active.remove(aid)
                elif record["target"] == scenario.TREASURER:
                    balance += record["amount"]
                cursor += 1
            target, amount = None, 0
            for task in scenario.TASKS:
                if paid[task["main"]] >= task["main_price"] or paid[task["backup"]] >= task["backup_price"]:
                    continue
                if task["main"] in active:
                    target, amount = task["main"], task["main_price"]
                elif task["backup"] in active:
                    target, amount = task["backup"], task["backup_price"]
                if not target or balance < amount + 20:
                    target = None
                break
            else:
                if scenario.FINANCE in active and balance > 20:
                    target, amount = scenario.FINANCE, balance - 20
            if target:
                balance -= amount
                paid[target] += amount
                payments.append((now, target, amount))
                self.assertGreaterEqual(balance, 20)
        self.assertEqual(20, balance)
        self.assertEqual({"agent-audit-backup": 80, "agent-storage-backup": 120,
                          "agent-compute-backup": 180, scenario.FINANCE: 200}, dict(paid))
        self.assertEqual(5, len(payments))
        for task in scenario.TASKS:
            completed_at = next(t for t, target, _ in payments if target == task["backup"])
            self.assertLessEqual(completed_at, task["deadline_ms"])
        self.assertTrue(any(t >= 450000 and target == scenario.FINANCE
                            for t, target, _ in payments))


if __name__ == "__main__":
    unittest.main()
