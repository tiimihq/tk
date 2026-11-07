from ledgerly.core.money import D, q


def prorate(total, qty, refunded_qty):
    """Refund for `refunded_qty` of `qty` units on a line costing `total`."""
    if refunded_qty > qty:
        raise ValueError("cannot refund more than was bought")
    return q(D(total) * D(refunded_qty) / D(qty))
