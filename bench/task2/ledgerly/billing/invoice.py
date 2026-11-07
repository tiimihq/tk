from ledgerly.billing.tax import tax_for
from ledgerly.core.money import D, q


def invoice_total(lines, state):
    subtotal = sum((D(p) * n for p, n in lines), D(0))
    return q(subtotal + tax_for(subtotal, state))
