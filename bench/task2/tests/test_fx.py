import unittest

from ledgerly.reports.export import receipt_rate_line


class FxTest(unittest.TestCase):
    def test_receipt_shows_four_places(self):
        self.assertEqual(receipt_rate_line("USD", "EUR"), "1 USD = 0.9211 EUR")
        self.assertEqual(receipt_rate_line("GBP", "EUR"), "1 GBP = 1.1726 EUR")
