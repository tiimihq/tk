from ledgerly.core.money import D, q


def store_credit_balance(entries):
    return q(sum((D(e) for e in entries), D(0)))
