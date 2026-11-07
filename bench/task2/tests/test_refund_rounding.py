import unittest
from decimal import Decimal

from ledgerly.refunds.calc import prorate


class ProrateTest(unittest.TestCase):
    def test_half_even(self):
        self.assertEqual(prorate(Decimal("0.25"), 2, 1), Decimal("0.12"))
        self.assertEqual(prorate(Decimal("0.75"), 2, 1), Decimal("0.38"))

    def test_too_many(self):
        with self.assertRaises(ValueError):
            prorate(Decimal("1"), 1, 2)
