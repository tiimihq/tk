import unittest
from decimal import Decimal

from ledgerly.billing.discounts import apply_discount
from ledgerly.billing.invoice import invoice_total


class BillingTest(unittest.TestCase):
    def test_invoice(self):
        self.assertEqual(invoice_total([("19.99", 2), ("5.00", 1)], "CA"), Decimal("48.24"))

    def test_discount(self):
        self.assertEqual(apply_discount("20.00", 15), Decimal("17.00"))
