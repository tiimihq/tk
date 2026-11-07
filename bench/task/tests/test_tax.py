import unittest
from shop.tax import tax_for, with_tax


class TaxTest(unittest.TestCase):
    def test_ca(self):
        self.assertEqual(tax_for(100.00, "CA"), 7.25)

    def test_half_even(self):
        # 8.50 * 6.25% = 0.53125 -> 0.53 ; 2.20 * 6.25% = 0.1375 -> 0.14
        self.assertEqual(tax_for(8.50, "TX"), 0.53)
        self.assertEqual(tax_for(2.20, "TX"), 0.14)

    def test_float_edge(self):
        # 66.90 * 4% = 2.676 -> 2.68 ; 1.0050 total case below
        self.assertEqual(tax_for(66.90, "NY"), 2.68)
        self.assertEqual(with_tax(2.675, "OR"), 2.68)

    def test_zero(self):
        self.assertEqual(tax_for(19.99, "OR"), 0.0)
