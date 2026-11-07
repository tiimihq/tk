# shop

Order maths for a small web shop. Money is in dollars as floats at the API edge,
but every stored amount must be rounded to cents with half-even (banker's)
rounding, the way our payment provider does it.

Tax rates live in `shop/rates.json`.

Run the tests: `python3 -m unittest`
