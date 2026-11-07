from decimal import Decimal, ROUND_HALF_EVEN

CENT = Decimal("0.01")


def D(x):
    """Any number to Decimal without float artifacts."""
    return x if isinstance(x, Decimal) else Decimal(str(x))


def q(x):
    """Round to cents, half-even. The one rounding rule for stored money."""
    return D(x).quantize(CENT, rounding=ROUND_HALF_EVEN)
