import unittest
from decimal import Decimal

from ledgerly.payments.settlement import settlement_amount


class SettlementTest(unittest.TestCase):
    def test_settles_in_usd(self):
        self.assertEqual(settlement_amount(Decimal("100.00"), "EUR"), (Decimal("108.57"), "USD"))
        self.assertEqual(settlement_amount(Decimal("10.00"), "GBP"), (Decimal("12.73"), "USD"))
