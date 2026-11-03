# dmf-app-tally-gateway

Distributes the tally state of a [SwitchFrame](https://github.com/zsiec/switchframe)
video switcher to tally lights over WebSocket.

The gateway polls `GET /api/switch/state` on SwitchFrame, keeps the latest
tally per source and pushes a `tally` message to every device whose source
changed. Each device registers with a device id and a source; an operator can
reassign it from the monitoring page or the management API, and the assignment
is pushed to the device at once and persisted across restarts.

When SwitchFrame state is older than `UPSTREAM_STALE_TIMEOUT`, every device
receives `valid:false` and shows fail-safe (LEDs off) until the next complete
snapshot arrives.

## Protocol

Devices speak tally protocol v1 on `/ws/v1`, specified in
[docs/protocol.md](docs/protocol.md).

## Configuration

Environment variables only. Durations use Go syntax (`50ms`, `3s`).

| Variable | Default | Meaning |
| --- | --- | --- |
| `SWITCHFRAME_URL` | required | SwitchFrame base URL, e.g. `http://switchframe:8080` |
| `SWITCHFRAME_TOKEN` | empty | Bearer token for the SwitchFrame API |
| `POLL_INTERVAL` | `50ms` | Interval between state polls |
| `UPSTREAM_STALE_TIMEOUT` | `3s` | Age after which upstream state is stale; also the poll request timeout |
| `HEARTBEAT_INTERVAL` | `10s` | Interval of `heartbeat` messages and WebSocket pings |
| `CLIENT_TIMEOUT` | `30s` | A device that sends nothing, not even a pong, for this long is disconnected |
| `DATA_DIR` | `/data` | Directory of the device registry, `devices.json` |
| `AUTH_TOKEN` | empty | When set, required on `/ws/v1`, and on `/api/v1` and the monitoring page unless OIDC signs a person in |
| `OIDC_ISSUER`, `OIDC_CLIENT_ID`, `OIDC_REDIRECT_URL` | empty | Sign people in on the monitoring page and `/api/v1` through this issuer, as this public client; the redirect URL is the page's public origin plus `/auth/callback`. Set all three or none |
| `LISTEN_ADDR` | `:8080` | HTTP listen address |
| `TLS_CERT_FILE`, `TLS_KEY_FILE` | empty | Serve HTTPS and WSS; set both or neither |

`AUTH_TOKEN` is accepted as `Authorization: Bearer <token>` or as the `token`
query parameter. The monitoring page is opened as `/?token=<token>` and passes
the token on to the API.

With OIDC, opening the page without a token sends the browser to the issuer:
the authorization code flow with PKCE, no client secret. A completed sign-in
starts a session of 12 hours in the cookie `tally_session`, held in memory, so
a restart signs people in again through the issuer's own session. An API call
without a session or the token is refused with 401; `/ws/v1` takes the token
only.

## HTTP endpoints

| Path | Purpose |
| --- | --- |
| `GET /` | Monitoring page: devices, sources, assignment |
| `GET /ws/v1` | Device WebSocket |
| `GET /auth/callback` | Completes an OIDC sign-in; served with OIDC only |
| `GET /api/v1/devices` | Devices with assigned source, firmware, connection state, last seen and current tally |
| `PUT /api/v1/devices/{id}` | Assign a source, body `{"sourceId":"cam2"}`; 404 for an unknown device |
| `DELETE /api/v1/devices/{id}` | Forget a device and close its connection |
| `GET /api/v1/sources` | SwitchFrame sources with label and tally, and whether upstream state is fresh |
| `GET /healthz` | Liveness |
| `GET /readyz` | 200 while SwitchFrame state is fresh, else 503 |
| `GET /metrics` | Prometheus metrics, prefixed `tally_gateway_` |

`/healthz`, `/readyz` and `/metrics` never require the token.

A device unknown to the registry is stored with the source it registers with.
From then on the stored source wins: a device registering with another source
is told its stored one in `registered`.

## Development

```sh
go test -race ./...
SWITCHFRAME_URL=http://localhost:8080 DATA_DIR=. LISTEN_ADDR=:8081 go run ./cmd/tally-gateway
```

## Deployment

The image is `ghcr.io/marquies/dmf-app-tally-gateway`, the chart
`oci://ghcr.io/marquies/dmf-app-tally-gateway/charts/tally-gateway`, both
published at the release version.

```sh
helm install tally oci://ghcr.io/marquies/dmf-app-tally-gateway/charts/tally-gateway \
  --version <version> --set switchframe.url=http://switchframe:8080
```

The chart runs exactly one replica with the `Recreate` strategy: device
connections, sequence numbers and the registry belong to one process. The
registry lives on a PersistentVolumeClaim that is kept on uninstall. See
[values.yaml](charts/tally-gateway/values.yaml) for tokens, TLS, ingress and the
optional ServiceMonitor.

## Releases

release-please opens a release PR from Conventional Commits on `main`; merging
it tags the release and publishes the image and the chart.
