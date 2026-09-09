# Server Design Spec — nxIIoT Gateway Ingestion API

Audience: the team building the **Internal Server** (the downstream system that receives sensor data from nxIIoT Gateway devices). This document specifies everything the server side must implement to correctly receive, acknowledge, and de-duplicate data from the gateway. It does not cover the gateway's internal architecture — only the wire contract between gateway and server.

A reference implementation of everything in this document (both transports) exists at `cmd/server-sim/main.go` in the gateway repo — it is a minimal but fully correct example of the required server behavior, used for the gateway's own local testing.

## 1. Architecture summary

- Each gateway device polls its local Modbus (RTU/TCP) sensors continuously and persists every reading to a local, durable queue (SQLite) **before** attempting to send it anywhere. This happens regardless of whether the server is reachable.
- A separate process on the gateway drains that local queue and delivers batches to the server, over **MQTT** (production/recommended) or **HTTP** (dev/test only — see §4).
- Delivery is **at-least-once**. The gateway will retry a batch it isn't sure was accepted, so the same reading can arrive at the server more than once. **The server must de-duplicate** — this is not optional (see §6).
- If the server is unreachable, the gateway does not stop collecting data — it keeps writing to its local queue and retries delivery once the server comes back. There is no data loss for the server to worry about on its side from a normal outage; the gateway's queue is the buffer.

## 2. Transports

| Transport | Status | Use |
|---|---|---|
| MQTT | Production | Recommended, has application-level acknowledgement, TLS, retained connection |
| HTTP | Dev/test only | Simple synchronous POST, **no authentication, no TLS support in the current gateway implementation** — do not use in production |

A given gateway is configured with exactly one transport at a time (not both simultaneously).

## 3. MQTT contract (production)

### 3.1 Topics

- **Data topic** (gateway → server): `gateway/{gateway_id}/data`
- **Ack topic** (server → gateway): `gateway/{gateway_id}/ack`

`{gateway_id}` is a free-form string identifying the specific gateway device (e.g. `GW002`, `MaxGate400`) — unique per deployed unit. There is no fixed format/pattern to validate against beyond "non-empty string".

**The server must subscribe to a wildcard**, not a single gateway's topic, since a real deployment has multiple gateways:

```
gateway/+/data
```

**The server must never hardcode a gateway_id when publishing an ack.** Derive the ack topic from the data topic the batch arrived on:

```
ack_topic = strip_suffix(received_topic, "/data") + "/ack"
```

This is the only correct way to compute it — do not assume the ack topic is your own single fixed string, and do not reconstruct it from the `gateway_id` field inside the message body (always derive it from the MQTT topic string itself).

### 3.2 QoS

Both the data publish and the ack publish use **QoS 1** (at-least-once at the MQTT transport level). Note QoS 1 only guarantees the *broker* received the message — it says nothing about whether your server *processed* it. That is exactly why the application-level ack below exists as a separate mechanism on top of QoS 1.

### 3.3 Message envelope (data topic)

```json
{
  "batch_id": "b3f1c2a4-...-uuid",
  "entries": [
    {
      "gateway_id": "GW002",
      "sequence_id": 481923,
      "device_id": 3,
      "datapoint_id": 17,
      "value": 231.4,
      "quality": "GOOD",
      "event_timestamp": "2026-09-09T08:22:41.795123456Z",
      "priority": "NORMAL"
    }
  ]
}
```

- `batch_id`: a UUID (v4-style string) generated fresh by the gateway for every publish attempt — including retries of the same underlying data. **Do not use `batch_id` for de-duplication of the underlying readings** — a retried batch gets a *new* `batch_id` each attempt even though its entries may be identical to a previous attempt. `batch_id` exists solely to correlate a publish with its ack (see §3.4). Real de-duplication is per-entry, keyed on `gateway_id` + `sequence_id` (§6).
- `entries`: an array of 1 or more readings (see §5 for field reference). Batch size is configurable on the gateway (default up to 100 entries per batch, tunable — do not assume a fixed maximum, but design to comfortably handle at least a few hundred entries in one message).

### 3.4 Ack envelope (ack topic)

On success:

```json
{ "batch_id": "b3f1c2a4-...-uuid" }
```

On failure/rejection (rare — for a genuine processing error on the server's side, not "duplicate", see below):

```json
{ "batch_id": "b3f1c2a4-...-uuid", "error": "human-readable reason" }
```

- The server must publish exactly one ack per batch it receives on the data topic, using the `batch_id` from that batch, to the ack topic derived per §3.1.
- **A duplicate batch (already-seen entries) is still a success from the ack's point of view.** Deduplicating an entry is not an error condition — ack normally (empty/no `error` field) even if every entry in the batch turned out to be a duplicate you already had. The gateway does not need or want to know which entries were duplicates; only whether the batch was accepted.
- Only report `error` for a genuine processing failure (e.g. malformed payload, a downstream dependency you need is down, etc.) — the gateway treats an ack with `error` set the same as never receiving an ack at all: the whole batch goes back to pending and will be retried later (see §7). If your error is really about "some entries look invalid," still ack success for the batch as a whole unless the entire batch is genuinely unusable — over-reporting errors just causes needless retries and does not give you a way to communicate per-entry problems anyway (the ack has no per-entry structure).

### 3.5 Timing

The gateway waits up to a configurable timeout (default **10 seconds**) after publishing for the corresponding ack to arrive. If your server cannot process and ack within that window under normal load, batches will start timing out and being retried (redundant work, not data loss — retries are idempotent per §6, but avoid it for efficiency). Ack promptly; do heavy processing asynchronously after acking if needed, as long as you're confident you've durably captured the batch before acking.

### 3.6 Connection / client ID

- The gateway's MQTT client ID defaults to its `gateway_id`. Most brokers enforce "last connection wins" per client ID — **do not** connect your own server-side tooling (debugging, test clients) using the same client ID as a live gateway, or you will kick the real device offline.
- The gateway auto-reconnects on its own (both via the MQTT client library's built-in retry and a gateway-side watchdog that forces a reconnect if the built-in retry ever silently stalls). You do not need to do anything special server-side to handle a gateway reconnecting — just keep your subscription live; the broker will keep delivering to it.

### 3.7 Authentication / TLS

Both are supported by the gateway and are configured per-deployment, so your broker/server setup should be prepared to offer whichever the operator configures:

- **Username/password**: plain MQTT username+password auth, optional.
- **TLS**: optional, with support for a custom CA file (for a private/internal CA), client certificate + key (mutual TLS), and an `insecure_skip_verify` escape hatch (should not be used in production — exists for lab/dev setups with self-signed certs).

There is no other auth mechanism (no token/JWT-over-MQTT, no API key) — access control is expected to be enforced at the broker level (ACLs on who may publish/subscribe to which `gateway/{id}/...` topics) plus TLS client certs if mutual auth is required.

## 4. HTTP contract (dev/test only)

Used only for local development and testing (`cmd/server-sim`'s `/ingest` endpoint is the reference). **Do not build production infrastructure around this transport** — it has no authentication and no TLS in the current gateway implementation (an HTTPS adapter is on the gateway's roadmap but not built yet).

- The gateway `POST`s to a single configured URL.
- Body: a **plain JSON array** of entries (not wrapped in a `{batch_id, entries}` envelope — that envelope is MQTT-specific, used there to correlate with the ack topic). Same entry shape as §5.
- Success: any 2xx HTTP status code. The gateway does not read the response body.
- Failure: any non-2xx status, a connection error, or a timeout — treated as a failed send and retried (§7), same as an MQTT ack failure/timeout.
- No ack round-trip — the HTTP response status *is* the acknowledgement. There is no separate "ack topic" concept for this transport.
- De-duplication (§6) still applies — the same at-least-once/retry behavior holds for HTTP too.

## 5. Entry field reference

Each entry (identical shape whether inside the MQTT envelope's `entries` array or the bare HTTP JSON array) represents one Modbus reading:

| Field | Type | Notes |
|---|---|---|
| `gateway_id` | string | Identifies the sending gateway. Part of the idempotency key. |
| `sequence_id` | integer (int64) | **Monotonically increasing per `gateway_id`**, assigned atomically at the moment the gateway persists the reading locally, and persisted across gateway restarts (never resets). This is the other half of the idempotency key. Not globally unique by itself — always use it together with `gateway_id`. |
| `device_id` | integer (int64) | Identifies the physical device/sensor within the gateway's own configuration. Meaningful only in combination with `gateway_id` — device IDs are not globally unique across different gateways. |
| `datapoint_id` | integer (int64) | Identifies the specific tag/register read from that device. Same caveat as `device_id` — scoped to the owning gateway. |
| `value` | number or `null` | The decoded reading. **`null`** when the read failed (see `quality` below) — a failed read is still reported (not silently dropped), just with no value. |
| `quality` | string enum | One of: `"GOOD"`, `"TIMEOUT"`, `"CRC_ERROR"`, `"DEVICE_OFFLINE"`, `"INVALID"`. Only `"GOOD"` should generally be treated as a trustworthy `value`; the others represent a failed/degraded read reported for observability, `value` will be `null` for these. |
| `event_timestamp` | string, RFC3339 (nanosecond precision), UTC | When the reading was actually taken on the gateway (not when it was sent, and not when the server receives it — those can differ from this by anywhere from milliseconds to the full length of a server outage). Always use this field for time-series ordering/storage, not receipt time. |
| `priority` | string enum | One of: `"CRITICAL"`, `"HIGH"`, `"NORMAL"`, `"LOW"`. Configured per-datapoint on the gateway; defaults to `"NORMAL"` if not explicitly set. Affects delivery *order* under backlog (§8) and which data the gateway's local storage-pressure policy protects first when its local disk/queue is under pressure — it does not require any special server-side handling beyond being stored/passed through faithfully.

## 6. Idempotency & de-duplication (critical — required, not optional)

Because delivery is at-least-once, **the same entry can and will arrive more than once** under normal operation (a network blip after publish but before ack, a timeout that was actually a slow-but-successful ack, a gateway restart mid-delivery, etc.).

**The de-duplication key is `(gateway_id, sequence_id)`.** Two entries with the same pair represent the same reading — keep the first, discard/ignore the rest, and still ack success (§3.4).

Minimal reference approach (see `cmd/server-sim/main.go`'s `store.ingest` for a working example): maintain a lookup keyed on `(gateway_id, sequence_id)`; for each incoming entry, check-and-insert; only entries that were newly inserted are "new" data to actually process/store/forward downstream. This must be durable (survive a server restart) in any real production implementation — an in-memory-only dedup table, as in the dev reference implementation, is not sufficient for production since a server restart would forget what it had already seen and cause a spike of reprocessed duplicates on the next few batches after any outage. A unique constraint on `(gateway_id, sequence_id)` in whatever the server's own persistent store is (with an ON CONFLICT-do-nothing / equivalent upsert) is the recommended pattern.

Do **not** attempt to de-duplicate on `batch_id` — as noted in §3.3, a retried batch gets a new `batch_id` each time even for identical underlying entries.

## 7. Delivery / retry behavior (what the gateway does — informs your timeout/error design)

- A batch is considered failed by the gateway if: the publish itself fails, the ack never arrives within the ack timeout (default 10s, §3.5), or the ack arrives with `error` set.
- On failure, the whole batch's entries go back to pending and are retried later with **exponential backoff**: 1s, 2s, 4s, 8s, 16s, 32s, then capped at 60s between attempts, per-entry (based on that entry's own individual retry count, so a batch can be reassembled from entries at different points in their own backoff schedules on a later attempt).
- Retries are not necessarily against the exact same batch grouping as the failed attempt — pending entries are re-batched each attempt.
- There is no maximum retry count / give-up point — the gateway keeps retrying indefinitely as long as the entry hasn't been acked. If your server is down for an extended period, expect a burst of catch-up traffic when it comes back, sized according to how much backlog accumulated (bounded by the gateway's local queue capacity, which is large — hundreds of thousands of rows by default).

## 8. Delivery ordering

Within a gateway's backlog, the gateway dispatches in this order: **priority first** (`CRITICAL` → `HIGH` → `NORMAL` → `LOW`), then **oldest first** (by `sequence_id`) within the same priority tier. This means:

- Under normal (non-backlogged) operation, entries arrive in roughly real-time order.
- Under a large backlog (e.g. after a long outage), you may receive a `CRITICAL` entry with a *later* `event_timestamp` before a `LOW`-priority entry with an *earlier* `event_timestamp` from the same gateway. **Do not assume received order equals chronological order** — always sort/key by `event_timestamp` (or `sequence_id` within a single `gateway_id`) for anything that depends on strict ordering, never by arrival order.
- Across different gateways there is no ordering guarantee or relationship at all — `sequence_id` is only meaningful scoped to its own `gateway_id`.

## 9. Multi-gateway checklist

A production server will be receiving from many gateways concurrently over the same broker/endpoint. Design for:

- Subscribing to the wildcard data topic (`gateway/+/data`), not a fixed list of known gateway IDs (new gateways may be provisioned over time).
- Deriving the ack topic per-message from the received topic (§3.1) — never a single shared/hardcoded ack topic.
- Treating `(device_id, datapoint_id)` as meaningful only in combination with `gateway_id`, never as globally unique identifiers on their own.
- Handling many gateways potentially reconnecting/catching-up around the same time (e.g. after a shared network segment or broker outage) — expect concurrent bursts across multiple `gateway_id`s, not just backlog from one.

## 10. Implementation checklist

- [ ] Subscribe to `gateway/+/data` at QoS 1
- [ ] Parse the `{batch_id, entries[]}` envelope
- [ ] De-duplicate every entry on `(gateway_id, sequence_id)` against durable storage
- [ ] Derive the ack topic from the received topic (`.../data` → `.../ack`), never hardcode it
- [ ] Publish `{"batch_id": "..."}` at QoS 1 once the batch is durably captured (dedup applied, persisted) — ack success even for all-duplicate batches
- [ ] Only set `error` in the ack for genuine processing failures, and understand that doing so triggers a full-batch retry from the gateway
- [ ] Sort/process by `event_timestamp` (or `sequence_id` within a `gateway_id`), never by arrival order
- [ ] Support TLS (custom CA / mutual TLS) and username+password auth on the broker side if the deployment requires them
- [ ] Load-test for backlog catch-up bursts (a gateway or broker outage of length T followed by recovery can deliver T-seconds'-worth of backlog in a short burst once reconnected)
- [ ] If an HTTP fallback is ever needed for a specific site: implement the same de-duplication logic against the bare JSON array on your ingest endpoint, understanding this path currently has no authentication or TLS on the gateway side — treat it as trusted-network-only, not for anything internet-facing
