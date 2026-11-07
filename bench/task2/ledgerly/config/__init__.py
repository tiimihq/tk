import json
import os

_DIR = os.path.dirname(__file__)


def load(name):
    with open(os.path.join(_DIR, name)) as f:
        return json.load(f)
