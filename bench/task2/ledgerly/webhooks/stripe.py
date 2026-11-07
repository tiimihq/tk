def parse(event):
    obj = event["data"]["object"]
    return {"id": obj["id"], "amount": obj["amount"] / 100, "currency": obj["currency"].upper()}
