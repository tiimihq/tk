from datetime import date

from ledgerly import settings


def check_window(order, today=None):
    today = today or order.get("today") or date.today()
    bought = order.get("bought", today)
    if (today - bought).days > settings.REFUND_WINDOW_DAYS:
        raise ValueError("refund window closed")


def restocking_fee(amount):
    return amount * settings.RESTOCKING_FEE_PCT / 100
