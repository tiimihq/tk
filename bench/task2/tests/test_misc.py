import unittest
from decimal import Decimal

from ledgerly.customers.credit import store_credit_balance
from ledgerly.subscriptions.proration import prorate


class MiscTest(unittest.TestCase):
    def test_subscription_proration(self):
        self.assertEqual(prorate("30.00", 10, 30), Decimal("10.00"))

    def test_credit(self):
        self.assertEqual(store_credit_balance(["1.10", "2.20"]), Decimal("3.30"))
