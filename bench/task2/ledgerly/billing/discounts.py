from ledgerly.core.money import D, q


def apply_discount(amount, pct):
    return q(D(amount) * (D(100) - D(pct)) / D(100))


def coupon_value(code):
    return {"WELCOME10": 10, "VIP20": 20}.get(code, 0)
