"""Pre-2022 float money helpers. Not used by new code; kept for the archive importer."""


def to_cents(amount):
    return int(round(amount * 100))


def refund_cents(total_cents, qty, refunded):
    return int(round(total_cents * refunded / qty))
