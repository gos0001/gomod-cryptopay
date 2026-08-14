# Chain API reference

Verified by live request, not by reading documentation. Every claim below is
either backed by a captured response or explicitly listed as unverified.

**Verified: 2026-08-14.** Public node behaviour changes without notice — treat
anything here as stale after a few months, and re-run the probes before trusting
a number that matters.

---

## TRON / TronGrid

Base: `https://api.trongrid.io`, API key in the `TRON-PRO-API-KEY` header.

### Incoming TRC20 transfers to one address

```
GET /v1/accounts/{address}/transactions/trc20
      ?only_to=true
      &limit=200
      &order_by=block_timestamp,desc
      &min_timestamp={cursor}
      &only_confirmed=true          # optional, see below
```

Response record — captured verbatim:

```json
{
  "transaction_id": "261d7e7a525fc80599791a990cb175d2fb2298bd08a165a2497143e1fbadb47c",
  "token_info": {
    "symbol": "LSE",
    "address": "TFbLNAqLCYCcUYogjueTbANU8kWwSqve4G",
    "decimals": 6,
    "name": "Liquid Stake Energy"
  },
  "block_timestamp": 1772649612000,
  "from": "TJQQLsfYvwK1gJyET4C7hvPdJ2YyNcAUbL",
  "to": "TMuA6YqfCeX8EhbfYEg5y7S4DqzSJireY9",
  "type": "Transfer",
  "value": "1"
}
```

**There is no block number.** The full key set is exactly
`block_timestamp, from, to, token_info, transaction_id, type, value`. This is the
single most consequential finding here: the original design assumed a block
number was present and, when it turned out not to be, planned one
`gettransactioninfobyid` call per newly seen transfer. Neither is needed — see
*Confirmation without block numbers* below.

Notes:

- `value` is a decimal string in the token's smallest units. `token_info.decimals`
  is alongside it, so a record is self-describing.
- Addresses are base58 here, unlike the events endpoint.
- There is no log index. One record is one transfer, so `log_index` stays 0 for
  TRON.

**`limit` caps at 200.** `limit=201` and `limit=500` both answer **HTTP 400 with
an empty body** — no JSON error to parse, which the client has to expect.

**Pagination** is by opaque fingerprint:

```json
"meta": {
  "at": 1786700752737,
  "fingerprint": "TmGrm87pzf4zaAujPjBkuGVzsoitXnZABM…",
  "links": { "next": "https://api.trongrid.io/v1/accounts/…&fingerprint=TmGrm87…" },
  "page_size": 2
}
```

`meta.links.next` is absent on the last page. Following it is simpler than
rebuilding the query, but it comes back without the API key header, so the client
must add it.

### `only_confirmed` really does mean irreversible

Established by a targeted probe rather than assumed. Method: take a transfer from
the global USDT event feed that is flagged `_unconfirmed`, confirm it sits above
the solidified head, then ask that recipient's TRC20 feed both ways.

```
fresh transfer tx=a843c8ecb6fabd69… block=85343306
  solidified head=85343288  margin=18 blocks above it
  present in default feed        : True
  present in only_confirmed feed : False
```

So `only_confirmed=true` excludes anything above the solidified head. It is the
provider's own finality line, and it can be relied on.

An earlier, sloppier probe appeared to show the opposite — a transfer above the
solidified head present in the confirmed feed. That was a race: the solidified
head had been read *before* the feed, and it advances one block every 3 seconds.
Worth recording because the same mistake is easy to make again.

`only_unconfirmed=true` also exists and returns the complement.

### Block heads

```
POST /wallet/getnowblock            -> block_header.raw_data.{number,timestamp}
POST /walletsolidity/getnowblock    -> the same shape, at the irreversible head
```

`raw_data` keys: `number, parentHash, timestamp, txTrieRoot, version, witness_address`.

The gap between the two heads was **18–19 blocks / 57 s** across every sample:

| head | solidified | gap |
|---|---|---|
| 85343230 | 85343211 | 19 blocks, 57 s |
| 85343263 | 85343244 | 19 blocks, 57 s |
| 85343282 | 85343264 | 18 blocks |

That confirms both the 3-second block time and the 19-block irreversibility depth
of TRON's consensus, without having to trust either from memory.

### Confirmation without block numbers

Because `/walletsolidity/getnowblock` gives the irreversible head's **timestamp**,
and the transfer feed gives each transfer's `block_timestamp`, finality is a
comparison of two timestamps:

```
transfer is irreversible  ⟺  block_timestamp <= solidified_head.timestamp
```

Cost per polling cycle is therefore **exactly two requests**, independent of how
many transfers arrived:

1. `GET …/transactions/trc20?only_to=true&min_timestamp={cursor}` — discovery,
   including not-yet-final transfers, which is what makes a `detected` state
   possible.
2. `POST /walletsolidity/getnowblock` — the finality line, which settles every
   payment already in storage by arithmetic, with no second feed read.

`only_confirmed=true` is a cross-check on this rather than the primary mechanism:
polling it would also work, but it cannot settle a payment recorded before the
current window without re-paginating.

### The events endpoint, and why it is not usable

`GET /v1/contracts/{contract}/events?event_name=Transfer` returns much richer
records — `block_number`, `event_index`, `_unconfirmed`, and decoded
`result.{from,to,value}`:

```json
{
  "block_number": 85343241,
  "block_timestamp": 1786700820000,
  "contract_address": "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t",
  "event_index": 0,
  "event_name": "Transfer",
  "result": {
    "from": "0x3bb66ddf2301bf0ca7adb8e75890ed2124508cac",
    "to": "0xa73651c9d150618c6debdeae5a626ceb755d711e",
    "value": "4999999999"
  },
  "result_type": { "from": "address", "to": "address", "value": "uint256" },
  "_unconfirmed": true
}
```

**But it cannot be filtered by an indexed parameter.** Both `filters={"to":…}`
and a bare `to=…` were accepted and silently ignored — five returned rows had
five distinct recipients. Filtering only works on contract and event name, which
for USDT means every transfer on the chain. Unusable for watching one address.

Recorded because it is the obvious thing to reach for on discovering that the
transfer feed has no block number, and it is a dead end.

Two useful facts fall out of it anyway: addresses here are hex without the `41`
prefix, and `_unconfirmed` is exactly "above the solidified head" — verified in
both directions (200 events above the head, all flagged; 50 events ten minutes
old, none flagged).

### Rate limits

| Limit | Value | Source |
|---|---|---|
| Requests per second, with key, within quota | **15** | [docs](https://developers.tron.network/reference/rate-limits), confirmed by probe |
| Free tier daily quota | **100 000** requests, max 3 keys | [TronGrid announcement](https://trongrid.zendesk.com/hc/en-us/articles/900005953386-TronGrid-Limited-Free-Plan-is-Live) |
| Over daily quota | throttled to roughly **5 qps**, *not* blocked | docs |
| No key at all | dynamic limiting, 403, 30-second block | docs |

A burst of 30 concurrent requests produced `15× 200, 9× 429, 6× 403`. The 429 body,
captured:

```json
{"Error":"The key exceeds the frequency limit(15), and the query server is suspended for 27 s"}
```

Three things matter here:

- **Exceeding 15 qps costs about 27–30 seconds of blackout**, not one rejected
  request. The per-second limiter must shape strictly below the ceiling rather
  than discover it.
- The error key is capitalised `Error`, not `error`, and unlike the `/v1/…`
  endpoints there is no `success` field. The client needs to read both shapes.
- **No quota headers.** The only custom response header is `x-trace-id`; nothing
  reports remaining daily quota, so the budget has to be counted locally.

Note the asymmetry with the local counter: the provider *throttles* past the daily
quota while `pkg/ratebudget` will *refuse*. Refusing is the more conservative
choice and keeps behaviour predictable, but the rationale is "stay inside the
plan", not "the provider would cut us off".

---

## BSC

### Public endpoints: alive is not the same as usable

Every candidate answered `eth_blockNumber`. Then `eth_getLogs`, with a narrow
filter — one contract, `Transfer` topic, a specific recipient in `topics[2]`:

| Endpoint | `eth_blockNumber` | `eth_getLogs` |
|---|---|---|
| `bsc-dataseed.binance.org` | ok | **-32005 `limit exceeded`** at every range |
| `bsc-dataseed1.bnbchain.org` | ok | **-32005** at every range |
| `bsc-dataseed4.bnbchain.org` | ok | **-32005** at every range |
| `bsc-rpc.publicnode.com` | ok | **ok** |
| `bsc.publicnode.com` | ok | **ok** |
| `bsc.drpc.org` | ok | unreachable under load |
| `bsc-mainnet.public.blastapi.io` | ok | -32000 |
| `bsc.blockrazor.xyz` | ok | -32000 |
| `bsc.meowrpc.com` | ok | -32000 |
| `1rpc.io/bnb` | ok | -32602 |
| `rpc.ankr.com/bsc` | needs auth | — |

**The `bsc-dataseed*` family refuses `eth_getLogs` unconditionally** — ranges of
10, 100, 500, 1000, 5000, 10 000 and 50 000 blocks all returned -32005. This is
not a result-count cap that a smaller range would satisfy.

Two consequences:

- The planned default of `bsc-dataseed.binance.org` would have produced a service
  that starts cleanly, reports itself healthy, and never sees a single payment.
- **A liveness check based on `eth_blockNumber` is worthless here.** Node health
  has to be judged by the call the watcher actually makes.

### `eth_getLogs` depth limit on free nodes

On `bsc-rpc.publicnode.com`, measured by walking the depth:

| Depth below head | Result |
|---|---|
| 500 … 9 000 | ok |
| 10 000, 12 000, 20 000 | -32602 `Archive requests require a personal token` |

The constraint is **how far back**, not how wide. Combined with the block time
below, roughly **67 minutes of log history** is available for free.

This contradicts an assumption in PLAN.md, which said a day of downtime means a
catch-up "taking minutes". It does not: the logs for that day are simply not
served. The watcher must detect that its cursor has fallen outside the available
window and say so loudly, rather than skipping the gap silently. Longer gaps need
a paid archive endpoint — which `bsc.rpc_urls` being a list already allows.

### Block time and finality

Measured over 60 seconds of chain clock: blocks 115867959 → 115868094, 135 blocks
across 61 seconds of block timestamps = **0.45 s per block**.

That is far from the 3 seconds the plan assumed, and it is post-Lorentz/Maxwell
behaviour rather than anything exotic.

The `finalized` block tag **works on every node tested, including the dataseed
nodes that cannot serve logs**, and its lag is tiny:

| Sample | head | finalized | lag |
|---|---|---|---|
| 1 | 115867845 | 115867842 | 3 blocks |
| 2 | 115868094 | 115868093 | 1 block |

So finality on BSC should come from the `finalized` tag, not from counting a fixed
number of confirmations — the tag is authoritative, costs one cheap call, and can
be fetched from a node that is useless for logs. A confirmation count stays only
as a fallback for an endpoint that does not serve the tag.

`safe` also resolves on publicnode and dataseed; dRPC answered error 15 for it.
Not needed, but recorded.

### `Transfer` topic

`keccak256("Transfer(address,address,uint256)")` computed locally:

```
0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef
```

This matches the widely published value, so the hash itself is settled.

**Confirmed on a real log (2026-08-14, Hardhat bench).** Every `eth_getLogs`
probe against public nodes returned zero rows, so the topic layout was left
unverified in the first pass. Against the local node it is now observed directly:

```
recipient topic: 0x00000000000000000000000070997970c51812dc3a010c7d01b50e0d17dc79c8

logs matching 'Transfer to us': 2
  block=2 logIndex=0 value=7250000000000000001
    topics[2]=0x00000000000000000000000070997970c51812dc3a010c7d01b50e0d17dc79c8
```

The recipient is in `topics[2]`, `PadTopic` output matches it byte for byte, and
the eighteenth decimal place of `7250000000000000001` survives the contract, the
log and the decode — which is the digit a float64 would have dropped.

---

## Request budget, recalculated

TRON, at two requests per cycle regardless of transfer volume:

| Poll interval | Requests/day | Share of the 100 000 quota |
|---|---|---|
| 2 s | 86 400 | 86 % — too close |
| 3 s | 57 600 | 58 % |
| **5 s (recommended)** | **34 560** | **35 %** |
| 10 s | 17 280 | 17 % |

The headroom absorbs pagination after downtime: catching up on a busy address
costs one extra request per 200 transfers.

Peak rate at 5 s is 0.4 requests/second against a 15 qps ceiling, so the
per-second limiter exists only to stop a catch-up loop from bursting into a
27-second suspension.

BSC has no documented request budget on the free nodes, only the depth limit and
whatever unpublished rate limiting each operator applies. The list of endpoints is
the mitigation.

---

## What this settles

| Question | Answer | Effect |
|---|---|---|
| Does the TRC20 feed carry a block number? | **No**, only `block_timestamp` | No per-transfer call. Finality by timestamp against the solidified head |
| Is `gettransactioninfobyid` needed per transfer? | **No** | Cycle cost is a flat 2 requests |
| Is `only_confirmed` finality? | **Yes**, verified | Available as a cross-check |
| `limit` ceiling | 200; above it, HTTP 400 with an empty body | Client must not expect JSON on that error |
| TRON irreversibility | 19 blocks / 57 s, observed | No `tron.confirmations` setting needed — the provider defines the line |
| TronGrid ceiling | 15 qps; breach suspends the key ~27 s | Shape below it; `tron.qps` default 10 |
| Default TRON interval | 5 s | 35 % of quota |
| BSC default endpoints | **publicnode, not dataseed** | dataseed cannot serve logs at all |
| BSC node health check | must exercise `eth_getLogs` | `eth_blockNumber` passes on unusable nodes |
| BSC log history on free nodes | ~9 000 blocks ≈ 67 min | Downtime beyond that needs a paid archive; the watcher must report the gap |
| BSC block time | 0.45 s | `log_range` 2000 ≈ 15 min per chunk |
| BSC finality | `finalized` tag, lag 1–3 blocks | Preferred over a confirmation count; count kept as fallback |
| `Transfer` topic0 | `0xddf252ad…b3ef` | Settled |
| Recipient position | `topics[2]`, observed on a real log | `PadTopic` verified against it |

Resulting configuration defaults, replacing the guesses in TODO.md:

```json
{
  "tron": {
    "api_url": "https://api.trongrid.io",
    "api_key": "…",
    "daily_request_budget": 100000,
    "qps": 10,
    "watch_interval": "5s"
  },
  "bsc": {
    "rpc_urls": [
      "https://bsc-rpc.publicnode.com",
      "https://bsc.publicnode.com"
    ],
    "watch_interval": "5s",
    "use_finalized_tag": true,
    "confirmations": 15,
    "reorg_depth": 64,
    "log_range": 2000
  }
}
```

`tron.confirmations` is gone deliberately: TRON's finality is the provider's
solidified head, not a number we choose.

---

## `removed` is not available to a polling watcher

Established on the Hardhat bench (2026-08-14), and it invalidates an assumption
the BSC watcher was built on.

`eth_getLogs` answers with the logs of the **canonical** chain. A log that a
reorg dropped is simply absent from the answer — there is no record of it with
`removed: true`. That flag belongs to log *subscriptions*, where the node reports
what it previously told you and is now retracting; a poller never told anything,
so nothing is retracted to it.

Demonstrated directly: `evm_snapshot`, a transfer, then `evm_revert`. Afterwards
the transfer's log was gone from `eth_getLogs` entirely, with no removed entry in
its place, and the watcher's revoke path never fired — the invoice stayed in
`detected`.

```
the reverted tx 0xe52b757a… is GONE, with no removed:true log emitted
```

The consequence is that reorg detection for a poller has to be **positive
absence**: re-read the last `reorg_depth` blocks each tick, and treat a
not-yet-final payment whose log no longer appears there as withdrawn.

Implemented that way, and verified on the same sequence that exposed the problem:

```
WARN  a credited transfer is no longer in the chain's logs; treating it as reorganised out
WARN  payment withdrawn by a reorg; the invoice is payable again
INFO  bsc tick  {"discovered": 0, "revoked": 1, ...}
```

The invoice returns to `pending`, the withdrawn payment keeps its row with
`removed_at` set, and the same invoice accepts a fresh payment afterwards because
the partial unique index excludes removed rows.

Costs no extra request in steady state: the window is 64 blocks against a
`log_range` of 2000, so it is the same single `eth_getLogs` call with an earlier
lower bound.

Worth recording plainly because the wrong version of this is easy to believe —
the field exists in the log object that `eth_getLogs` returns, it is just always
false.

## Still unverified

Recorded rather than quietly assumed.

- **The exact `eth_getLogs` result-count cap** on a node that serves logs. The
  depth limit was found; a result cap was never hit because the recipient filter
  makes results sparse. A busy shared receiving address could still hit one.
- **TRON reorg behaviour.** Nothing here exercises a transfer being un-mined.
  Blocks above the solidified head are by definition reversible, but the watcher's
  `detected → pending` path is untested on TRON and there is no cheap local node
  to test it on.
- **Behaviour past the daily quota.** The documented throttle to ~5 qps was not
  reproduced — doing so would mean spending 100 000 requests.
- **Whether the free publicnode endpoints rate-limit by IP over time.** Only short
  bursts were run.
- **`meta.links.next` under load**, and whether a fingerprint expires. Pagination
  was exercised shallowly.
