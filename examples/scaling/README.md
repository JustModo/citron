# Scaling

citron replicas share no state: a request carries everything it needs, and the compile
cache is a per-replica speed-up. Scaling out means running more replicas behind a load
balancer.

```sh
docker compose -f examples/scaling/compose.yaml up -d --build
docker compose -f examples/scaling/compose.yaml up -d --scale citron=5   # resize
docker compose -f examples/scaling/compose.yaml down
```

HAProxy listens on `127.0.0.1:2358` and discovers replicas through Docker DNS, so it
needs no access to the Docker socket.

## Load balancer requirements

These apply to any load balancer, not only HAProxy:

- **Least connections.** A request stays open until its submission is judged, so open
  connections measure load. Round robin piles work onto replicas that are already busy.
- **Health check `/ready`.** A replica stops reporting ready when it shuts down, so it
  is drained before it stops.
- **Timeouts above `server.write_timeout_seconds`** (60s by default).
- **Retry `503` on the client.** A replica that is full answers `503` rather than queue
  work it cannot finish in time.
