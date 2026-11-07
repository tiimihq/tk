from ledgerly import settings
from ledgerly.payments.fx import convert


def settlement_amount(amount, currency):
    """What the processor settles for a payment."""
    if settings.SETTLE_IN_USD:
        return convert(amount, currency, "USD"), "USD"
    return amount, currency
