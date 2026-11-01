# Tally WebSocket protocol v1 (shared contract, gateway <-> device)

Endpoint: `ws[s]://<host>:<port>/ws/v1`. Text frames, one JSON object each.
Every message carries `"version": 1` and `"type"`. Unknown fields are ignored;
unknown `type` from a client yields an `error` and the connection stays open.

Optional auth: when the gateway has a device token configured, the client sends
`Authorization: Bearer <token>` on the upgrade request (or `?token=<token>` as a
fallback for clients that cannot set headers). Missing/wrong -> HTTP 401, no upgrade.

## Client -> gateway

register (first message; must arrive within 10 s of connect or the gateway closes):
{"version":1,"type":"register","deviceId":"tally-esp32-001","sourceId":"camera-01","firmware":"1.0.0"}
- deviceId: required, non-empty, [A-Za-z0-9._-]{1,64}
- sourceId: required, non-empty (the SwitchFrame source key the device was configured with)
- firmware: optional string
A second connection with the same deviceId replaces the first (the older socket is closed).

## Gateway -> client

registered (reply to register):
{"version":1,"type":"registered","deviceId":"tally-esp32-001","sourceId":"camera-01",
 "session":"8f3a...","heartbeatIntervalMs":10000,"timeoutMs":30000}
- sourceId here is AUTHORITATIVE: the gateway's persisted assignment for the
  device wins over what the device registered with. The device must display tally
  for this sourceId. The gateway may send `registered` again at any time (an
  operator reassigned the device); the device then switches source.
- session: random id per gateway process start. Sequence numbers are only
  comparable within one session.
- timeoutMs: the fail-safe timeout the device should use (device config may override).

tally (immediately after every `registered`, then on every change for that source):
{"version":1,"type":"tally","sourceId":"camera-01","program":true,"preview":false,
 "state":"program","valid":true,"session":"8f3a...","sequence":1042,
 "timestamp":"2026-11-01T16:00:00Z"}
- state: raw SwitchFrame tallyState value: program | preview | key_fill | key_alpha | idle,
  or "unknown" when the source is not present in SwitchFrame.
- program = state in {program, key_fill}; preview = state == preview.
- valid=false means the gateway has no fresh upstream state (SwitchFrame stale/unreachable);
  program and preview are then both false. The device must show fail-safe (LEDs off).
- sequence: monotonically increasing across ALL messages the gateway emits in one session.
  Device discards a tally whose session equals the last seen session and whose
  sequence <= last applied sequence. A new session resets the comparison.

heartbeat (every heartbeatIntervalMs to each registered client):
{"version":1,"type":"heartbeat","session":"8f3a...","sequence":1043,"timestamp":"..."}

error:
{"version":1,"type":"error","code":"invalid_register","message":"deviceId is required"}
codes: invalid_message, invalid_register, not_registered, unsupported_version, unknown_type

## Supervision

- Gateway also sends WebSocket ping frames every heartbeatIntervalMs and closes a
  client that has sent nothing (no pong, no message) for timeoutMs.
- Device: any received `registered`, `tally` or `heartbeat` resets its fail-safe
  timer. If the timer exceeds timeoutMs (default 30 s), or the socket closes, or
  Wi-Fi drops, LEDs go OFF immediately; it reconnects and re-registers.

## Device LED mapping (reference two-LED device)

program -> red ON, green OFF; preview only -> green ON; both -> red ON, green OFF;
neither / valid=false / timeout / disconnected -> both OFF.

## Gateway behaviour where the contract leaves room

- While upstream state is stale, `tally` carries `"state":"unknown"` along with
  `valid:false`: no state is known.
- A source SwitchFrame does not list yields `"state":"unknown"` with
  `valid:true` while upstream state is fresh.
- `registered` and `error` carry no sequence number; `tally` and `heartbeat`
  share one counter.
- `not_registered` is never sent: `register` is the only client message type, so
  anything else is `unknown_type`.
- `register` on a connection already registered under another deviceId yields
  `invalid_register`; under the same deviceId it is answered like a first one.
- `timestamp` is RFC 3339 in UTC with second precision.
- Deleting a device through the management API closes its connection.
