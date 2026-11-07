"""Old refund flow (v1 API). Superseded by ledgerly.refunds.service."""
from ledgerly.legacy.money_old import refund_cents, to_cents


def refund_v1(total, qty, refunded):
    return refund_cents(to_cents(total), qty, refunded) / 100
