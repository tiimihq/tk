from ledgerly.payments.gateway import Gateway

_gw = Gateway()


def pay(order):
    return _gw.capture(order["id"], order["total"], order["currency"])
