def refund_email(order, amount):
    return f"Your refund of {amount} {order['currency']} for order {order['id']} is on its way."
