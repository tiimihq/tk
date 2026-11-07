from ledgerly.refunds.service import refund_amount


def post_refund(order, qty):
    return {"order": order["id"], "amount": str(refund_amount(order, qty)), "currency": order["currency"]}
