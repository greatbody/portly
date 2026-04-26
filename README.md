# portly

> Self-hosted, password-protected, dynamic reverse proxy for your internal services.

A lightweight single-binary HTTP(S) gateway that lets you expose any internal `IP:Port` web service to the public via a simple, authenticated path-based URL like:

```
http(s)://your-domain/p/{slug}/
```

## Features (V1)

- Path-based reverse proxy: `/p/{slug}/*`
- WebSocket passthrough
- HTML / Cookie / Redirect rewriting so apps work behind a sub-path
- **SPA Referer Rescue**: Transparently rewrites absolute URLs and dynamic imports using Referer headers and cookies
- **SSRF Protection**: Restricts upstream proxy targets to private network IPs / allowed CIDRs and blocks sensitive ports
- Single admin login (session cookie, argon2id password hashing)
- Web admin panel to add / edit / delete / enable targets
- SQLite single-file storage
- Listens on `0.0.0.0`, plain HTTP supported (TLS via reverse-proxy front later)
- Single Go binary, no runtime dependencies

## Quick start

```bash
go build -o portly ./cmd/portly
cp config.example.yaml config.yaml   # edit listen / admin password
./portly --config config.yaml
```

Open `http://<server-ip>:<port>/` and log in. If `admin.password` is empty in the
config, a random password is generated and printed to stdout on first launch.

### systemd

A unit file is provided in `systemd/portly.service`. Adjust paths and user, then:

```bash
sudo cp portly /opt/portly/portly
sudo cp config.yaml /opt/portly/config.yaml
sudo cp systemd/portly.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now portly
```

## URL scheme

```
/                    → dashboard (login required)
/login               → login page
/admin/*             → target management UI
/api/*               → JSON API
/p/{slug}/*          → reverse proxy to a registered target
```

## License

MIT

## Current dev instance (this machine)

For local verification, the dev instance shipped with `config.yaml` listens on:

| | |
|---|---|
| Listen | `0.0.0.0:18080` |
| Loopback URL | http://127.0.0.1:18080/ |
| LAN URL | http://10.10.11.59:18080/ |
| Username | `admin` |
| Password | `portly-admin-2026` |

> Change `admin.password` in `config.yaml` before exposing publicly.
