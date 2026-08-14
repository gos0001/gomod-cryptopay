# How a transfer is matched to an invoice

There is one receiving address per network and no private keys, so an invoice
cannot be identified by where the money went. It is identified by **how much**.

```
pay_amount = requested_amount + nonce * asset.step
```

`nonce` is the lowest number whose amount is not currently reserved for that
asset. A transfer of value `V` settles the invoice when:

```
pay_amount <= V < pay_amount + step
```

The upper bound is exclusive because `pay_amount + step` is, by construction, some
other invoice's amount. The window is what makes a transfer unambiguous.

## Consequences you will meet in practice

**A payer who sends the requested amount instead of `pay_amount` is not credited.**
Their transfer falls below the window. It becomes an orphan transfer, visible at
`GET /api/v1/orphans`, and somebody has to decide what to do about it.

**Underpayment is never partial credit.** `V` below the window is an orphan, full
stop. Same for overpayment beyond `pay_amount + step`.

**An amount stays reserved after its invoice ends**, for `invoices.amount_hold`.
A transfer sent two minutes before expiry can confirm ten minutes after it, and it
must not be applied to whichever new invoice inherited the amount. This is why a
cancelled invoice does not immediately free its amount, and why `amount_hold`
should comfortably exceed the slowest confirmation you expect.

**Reuse is gated by a boolean, not by the hold timestamp.** If the expiry job is
not running, amounts are never released and invoice creation eventually fails with
no free amount. Failing that way round is deliberate: the alternative is
misapplied money.

## Choosing `step`

`step` is a decimal string in the token's **smallest units**, and it is the
increment between two invoices' amounts.

At 6 decimals (TRC20 USDT), for a price of 10.50 and nonce 3:

| `step` | Increment | Payer sees | Band at `nonce_max` 1000 |
|---|---|---|---|
| `"100"` | 0.0001 USDT | 10.5003 | 0.1 USDT |
| `"1000"` | 0.001 USDT | 10.503 | 1 USDT |
| `"10000"` | 0.01 USDT | 10.53 | 10 USDT |

The default, when `step` is omitted, is four decimal places — `"100"` at 6
decimals, `"100000000000000"` at 18.

Two forces pull against each other:

- **Too small** and rounding on the payer's side, or a wallet that truncates,
  produces a transfer that misses the window. Exchange withdrawals are the usual
  offender.
- **Too large** and the offset stops being negligible: at `step` = 0.01 and
  `nonce_max` = 1000 the amounts span 10 whole tokens above the price.

The band you must be able to accept is `step * nonce_max`. Pick `nonce_max` from
how many invoices can be live at once for one asset — including the ones still in
their hold window, not just the payable ones.

`decimals` must match the token contract. USDT is 6 on TRON and 18 on BSC; getting
it wrong scales every amount by a power of ten.

## Why amounts are strings everywhere

Every amount in the API is a decimal string, and internally an integer number of
smallest units — `NUMERIC(78,0)` in Postgres, `big.Int` in Go. A JSON number is a
float64, and 18 significant decimals do not fit in one. A rounded amount is not a
near miss here: it is outside the matching window, so it is an orphan.

`GET /api/v1/assets` returns each asset's `step` and `decimals`, so a client can
render amounts without hardcoding either.

## Debugging "the payment was not credited"

In order:

1. Compare the transfer's value against `pay_amount_units` — exact integer
   comparison, not decimal maths. Below the window is the common answer.
2. Check `GET /api/v1/orphans` for the transaction hash. If it is there, matching
   worked as designed and the amount was wrong.
3. Check the invoice status. `detected` means seen and waiting for confirmations;
   `confirmed` is the only paid.
4. Check the token contract in the transfer against the configured `assets`
   entry — a same-symbol impostor contract is not credited, which is the point.
5. Check the receiving address. Money sent elsewhere is not visible to a watch-only
   service at all.
