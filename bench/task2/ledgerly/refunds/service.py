from ledgerly import settings
from ledgerly.payments.fx import convert
from ledgerly.refunds.calc import prorate
from ledgerly.refunds.policy import check_window


def refund_amount(order, refunded_qty):
    """Money back to the customer, in the order's currency."""
    check_window(order)
    total, cur = order["total"], order["currency"]
    if settings.SETTLE_IN_USD:
        # refunds go through the processor, which works in USD
        usd = convert(total, cur, "USD")
        back = prorate(usd, order["qty"], refunded_qty)
        return convert(back, "USD", cur)
    return prorate(total, order["qty"], refunded_qty)
