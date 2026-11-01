# dmf-app-tally-gateway

## Protocol

`docs/protocol.md` is the device contract. Firmware is written against it, so a
change to the wire format is a protocol change: either backwards compatible or a
new version on a new path, and the document changes in the same commit.

## Upstream

SwitchFrame is read through `upstream.Source` only. The poller exists because
SwitchFrame offers no push interface reachable without changes to it; a push
source replaces the poller behind that interface, nothing else changes.
Decode only the fields of `ControlRoomState` the gateway uses.

## Invariants

- Every message with a sequence number is numbered and queued under `Hub.mu`.
  Numbering outside that lock lets a device receive sequence numbers out of
  order and discard a newer tally.
- Nothing blocks on a device: queues are bounded and a full one drops the
  device.
- Stale upstream state lights nothing. Validity returns only with a complete
  snapshot.
- One instance. The chart keeps `replicas: 1` and `Recreate`.

## Before finish

`go vet ./...`, `go test -race ./...`, `docker build .` and
`helm lint charts/tally-gateway --set switchframe.url=http://x` pass.
