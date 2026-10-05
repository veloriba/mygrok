# E2E test stack

Local-only three-container smoke test for the multi-tunnel client. Placeholders
only (`test.local`, `testtoken`) — safe to keep in the repo.

| Container | Image | Role |
|---|---|---|
| `mygrok-e2e-server-1` | `mygrok:0.3.0` (repo Dockerfile, `make docker-build`) | tunnel server, domain `test.local` |
| `mygrok-e2e-client-1` | `mygrok-client-test:0.3.0` (see `Dockerfile.client`) | one client process running **two** tunnels (`api`, `web2` → `tunnels.json`) + python upstream on `127.0.0.1:8099` |
| `mygrok-e2e-user-1` | `curlimages/curl` | end user: `docker exec` in here and curl through the tunnels |

## Usage (from the repo root)

```sh
# rebuild images
make docker-build
docker build -f e2e-tests/Dockerfile.client -t mygrok-client-test:0.3.0 .

# start / inspect / stop
docker compose -f e2e-tests/docker-compose.yml up -d
docker compose -f e2e-tests/docker-compose.yml logs client
docker exec mygrok-e2e-user-1 sh -c "curl -s -H 'Host: api.test.local' http://server:8080/"
docker compose -f e2e-tests/docker-compose.yml down
```

Both curls (`api.test.local`, `web2.test.local`) must return `UPSTREAM-OK`.
