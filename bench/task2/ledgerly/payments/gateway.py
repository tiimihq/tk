from ledgerly.payments.settlement import settlement_amount


class Gateway:
    def __init__(self):
        self.captured = []

    def capture(self, order_id, amount, currency):
        settled, cur = settlement_amount(amount, currency)
        self.captured.append((order_id, settled, cur))
        return settled, cur
