def parse(event):
    r = event["resource"]
    return {"id": r["id"], "amount": float(r["amount"]["value"]), "currency": r["amount"]["currency_code"]}
