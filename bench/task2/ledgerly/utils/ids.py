import itertools

_counter = itertools.count(1)


def next_id(prefix):
    return f"{prefix}_{next(_counter):06d}"
