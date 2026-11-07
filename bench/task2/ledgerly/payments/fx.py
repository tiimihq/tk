from decimal import Decimal, ROUND_HALF_EVEN

from ledgerly.config import load
from ledgerly.core.money import D, q

RATE_PLACES = Decimal("0.0001")  # receipts show 4 decimal places; keep in sync with reports


def rate(frm, to):
    """Units of `to` per unit of `frm`, at receipt precision."""
    if frm == to:
        return Decimal(1)
    usd_per = {k: Decimal(v) for k, v in load("fx_rates.json").items()}
    return (usd_per[frm] / usd_per[to]).quantize(RATE_PLACES, rounding=ROUND_HALF_EVEN)


def convert(amount, frm, to):
    return q(D(amount) * rate(frm, to))
