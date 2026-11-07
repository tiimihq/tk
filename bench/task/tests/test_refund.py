import unittest
from shop.refund import line_refund, order_refund


class RefundTest(unittest.TestCase):
    def test_simple(self):
        self.assertEqual(line_refund(10.00, 3, 1), 10.00)

    def test_discount(self):
        self.assertEqual(line_refund(20.00, 2, 2, discount_pct=10), 36.00)

    def test_half_even(self):
        # 2.675 is stored as 2.67499999... as a float; refunds must still give 2.68
        self.assertEqual(line_refund(2.675, 1, 1), 2.68)
        # 0.125 -> 0.12 (half-even), 0.375 -> 0.38
        self.assertEqual(line_refund(0.125, 1, 1), 0.12)
        self.assertEqual(line_refund(0.375, 1, 1), 0.38)

    def test_order(self):
        lines = [(19.99, 2, 1, 15), (5.05, 4, 4, 0), (1.005, 1, 1, 0)]
        # 16.9915 -> 16.99 ; 20.20 ; 1.005 -> 1.00 (half-even) => 38.19
        self.assertEqual(order_refund(lines), 38.19)
