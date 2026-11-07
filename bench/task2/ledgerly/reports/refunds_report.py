from ledgerly.refunds.calc import prorate


def refund_rows(orders):
    return [(o["id"], prorate(o["total"], o["qty"], o["refunded"])) for o in orders]
