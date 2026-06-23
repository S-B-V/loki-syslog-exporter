# loki-syslog-exporter

Tiny Go CLI/container that exports Loki logs for `service_name="syslog"`.

Defaults:

- Loki URL: `http://localhost:3100`
- Query: `{service_name="syslog"}`
- Range: last `24h`
- Output file: `syslog_last_24h.log`
- Pagination limit: `5000`

## Local build

```bash
go build -o loki-syslog-exporter ./cmd/loki-syslog-exporter
```

## Run locally

```bash
kubectl -n monitoring port-forward svc/loki 3100:3100
```

In another terminal:

```bash
./loki-syslog-exporter
```

Export from a specific date:

```bash
./loki-syslog-exporter \
  -start "2026-06-05 00:00:00" \
  -out syslog-since-june-5.log
```

Supported date formats:

- `2026-06-05`
- `2026-06-05 00:00:00`
- `2026-06-05T00:00:00Z`
- RFC3339 / RFC3339Nano

## Docker / Podman

Build:

```bash
docker build -t loki-syslog-exporter .
```

If Loki is reached through a `kubectl port-forward` on your host, use host networking on Linux:

```bash
docker run --rm --network host --user "$(id -u):$(id -g)" -v "$PWD:/data" ghcr.io/s-b-v/loki-syslog-exporter:latest
```

With Podman on Fedora / SELinux:

Retrieve UID/GID requirements:

```bash
podman run --rm --network host --userns=keep-id --entrypoint id ghcr.io/s-b-v/loki-syslog-exporter:latest
```
output:
```bash
uid=65532(app) gid=65532(app) groups=1000(sbv),65532(app)
```

Create folders:

```bash
mkdir -p logs
podman unshare chown -R 65532:65532 $PWD/logs

```


```bash
podman run --rm --network host --userns=keep-id -v "$PWD/logs/:/data:Z" ghcr.io/s-b-v/loki-syslog-exporter:latest
```

Specific start date and output file:

```bash
docker run --rm --network host --user "$(id -u):$(id -g)" -v "$PWD:/data" \
  -e START="2026-06-05 00:00:00" \
  -e OUT="syslog-since-june-5.log" \
  ghcr.io/s-b-v/loki-syslog-exporter:latest
```

If the container runs inside Kubernetes, point it at the in-cluster service instead, for example:

```bash
-e LOKI_URL="http://loki.monitoring.svc:3100"
```

## Flags / environment variables

| Flag | Env var | Default | Description |
|---|---|---:|---|
| `-url` | `LOKI_URL` | `http://localhost:3100` | Base Loki URL |
| `-query` | `LOKI_QUERY` | `{service_name="syslog"}` | LogQL query |
| `-start` | `START` | empty | Start time; empty means `end - last` |
| `-end` | `END` | now | End time |
| `-out` | `OUT` | `syslog_last_24h.log` | Output file |
| `-last` | `LAST` | `24h` | Relative range if `start` is empty; supports `24h`, `7d`, etc. |
| `-limit` | `LIMIT` | `5000` | Loki query_range page size |
| `-timeout` | `TIMEOUT` | `5m` | Timeout per request |
| `-tenant-id` | `TENANT_ID` | empty | Optional `X-Scope-OrgID` tenant |
| `-bearer` | `LOKI_BEARER_TOKEN` | empty | Optional bearer token |

## GHCR image

The included GitHub Actions workflow builds and pushes:

```text
ghcr.io/s-b-v/loki-syslog-exporter:latest
ghcr.io/s-b-v/loki-syslog-exporter:<branch>
ghcr.io/s-b-v/loki-syslog-exporter:sha-<git-sha>
```