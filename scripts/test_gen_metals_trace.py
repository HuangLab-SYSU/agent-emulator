"""Validate the checked-in workload and the buyer's fallback opportunity."""

import json
import unittest

import gen_metals_trace as scenario


class MetalsScenarioTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        with scenario.DEFAULT_OUT.open(encoding="utf-8") as source:
            cls.records = [json.loads(line) for line in source]

    def test_fixture_keeps_the_background_workload_and_leaves_purchases_to_the_api(self):
        self.assertEqual(self.records, scenario.generate())
        self.assertEqual(22000, sum(
            row["request_id"].startswith("background-") for row in self.records
        ))
        self.assertEqual(["join"], [
            row["action"] for row in self.records if row["agent_id"] == scenario.BUYER
        ])
        self.assertEqual(["join", "leave", "join"], [
            row["action"] for row in self.records if row["agent_id"] == scenario.GOLD
        ])

    def test_funding_supports_two_purchases_in_each_supplier_phase(self):
        # Reference buyer with immediate settlement, checked at each trace
        # event. The actual run uses API decisions and on-chain settlement.
        active = set()
        balance = 15
        purchases = []
        for row in self.records:
            agent = row["agent_id"]
            if row["action"] == "join":
                active.add(agent)
            elif row["action"] == "leave":
                active.remove(agent)
            elif row["target"] == scenario.BUYER:
                balance += row["amount"]

            if scenario.GOLD in active:
                target, price = scenario.GOLD, 160
            elif scenario.SILVER in active:
                target, price = scenario.SILVER, 100
            else:
                continue
            if balance >= price:
                balance -= price
                purchases.append((row["ts"], target, price, balance))

        self.assertEqual([
            (120000, scenario.GOLD, 160, 10),
            (170000, scenario.GOLD, 160, 10),
            (270000, scenario.SILVER, 100, 10),
            (320000, scenario.SILVER, 100, 10),
            (420000, scenario.GOLD, 160, 10),
            (470000, scenario.GOLD, 160, 10),
        ], purchases)
        self.assertEqual(10, balance)


if __name__ == "__main__":
    unittest.main()
