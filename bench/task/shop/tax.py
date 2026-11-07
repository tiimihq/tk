import json
import os

_RATES = json.load(open(os.path.join(os.path.dirname(__file__), "rates.json")))


def tax_for(amount, state):
    """Sales tax on `amount` (dollars) for a US state, rounded to cents."""
    rate = _RATES[state]
    return round(amount * rate / 100, 2)


def with_tax(amount, state):
    return round(amount + tax_for(amount, state), 2)
