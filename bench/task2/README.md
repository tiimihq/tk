# ledgerly

Billing, payments and refunds for the shop. Money is `Decimal`, rounded to cents
half-even (banker's rounding) everywhere, via `ledgerly.core.money`.

Settlement with the payment processor happens in USD (`settings.SETTLE_IN_USD`).
FX rates are kept at 4 decimal places, which is also what customers see on receipts.

    python3 -m unittest
