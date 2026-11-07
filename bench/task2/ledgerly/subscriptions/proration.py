"""Proration of subscription charges by days used (not for refunds of goods)."""
from ledgerly.core.money import D, q


def prorate(amount, days_used, days_in_period):
    return q(D(amount) * D(days_used) / D(days_in_period))
