from decimal import Decimal

from ledgerly.core.money import D, q

RATES_BPS = {"CA": 725, "NY": 400, "TX": 625, "OR": 0}


def tax_for(amount, state):
    return q(D(amount) * Decimal(RATES_BPS[state]) / Decimal(10000))
