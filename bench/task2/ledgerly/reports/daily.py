from ledgerly.core.money import D, q


def daily_total(payments):
    return q(sum((D(p["amount"]) for p in payments), D(0)))
