from ledgerly.utils.ids import next_id


def create_order(total, qty, currency):
    return {"id": next_id("ord"), "total": total, "qty": qty, "currency": currency}
