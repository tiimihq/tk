import unittest
from datetime import date
from decimal import Decimal

from ledgerly.refunds.service import refund_amount


def order(total, qty, currency):
    return {"id": "o1", "total": Decimal(total), "qty": qty, "currency": currency,
            "bought": date(2026, 1, 1), "today": date(2026, 1, 5)}


class RefundTest(unittest.TestCase):
    def test_usd(self):
        self.assertEqual(refund_amount(order("10.00", 4, "USD"), 1), Decimal("2.50"))

    def test_partial_refund_foreign_currency(self):
        # Support ticket #4471: EU customers get refunds a cent off.
        # The customer gets back exactly their share of what they paid, in their currency.
        self.assertEqual(refund_amount(order("10.00", 4, "EUR"), 1), Decimal("2.50"))
        self.assertEqual(refund_amount(order("10.07", 3, "EUR"), 1), Decimal("3.36"))
        self.assertEqual(refund_amount(order("10.07", 4, "GBP"), 1), Decimal("2.52"))

    def test_window(self):
        o = order("10.00", 1, "USD")
        o["today"] = date(2026, 3, 1)
        with self.assertRaises(ValueError):
            refund_amount(o, 1)
