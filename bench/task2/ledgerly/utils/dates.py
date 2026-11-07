from datetime import date, timedelta


def days_between(a, b):
    return (b - a).days


def add_days(d, n):
    return d + timedelta(days=n)


def month_start(d):
    return date(d.year, d.month, 1)
