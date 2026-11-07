from ledgerly.payments.fx import rate


def receipt_rate_line(frm, to):
    """The FX line printed on receipts: 4 decimal places, always."""
    return f"1 {frm} = {rate(frm, to)} {to}"
