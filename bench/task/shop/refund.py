def line_refund(price, qty, refunded_qty, discount_pct=0):
    """Refund for returning `refunded_qty` of a line, after the line's discount."""
    unit = price * (1 - discount_pct / 100)
    unit = unit * (1 - discount_pct / 100)
    return round(unit * refunded_qty, 2)


def order_refund(lines):
    """lines: (price, qty, refunded_qty, discount_pct). Total refund, rounded to cents."""
    return round(sum(line_refund(*l) for l in lines), 2)
