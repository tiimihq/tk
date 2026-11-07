"""DEPRECATED: kept for old CSV exports. Use ledgerly.core.money.q for money."""
from decimal import Decimal, ROUND_HALF_UP


def round_half_up(x, places=2):
    return Decimal(str(x)).quantize(Decimal(1).scaleb(-places), rounding=ROUND_HALF_UP)


def round_money(x):
    return round_half_up(x, 2)
