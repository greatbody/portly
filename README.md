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
- Single admin login (session cookie, argon2id password hashing)
- Web admin panel to add / edit / delete / enable targets
- SQLite single-file storage
- Listens on `0.0.0.0`, plain HTTP supported (TLS via reverse-proxy front later)
- Single Go binary, no runtime dependencies

## Quick start

```bash
go build -o portly ./cmd/portly
./portly --config config.yaml
```

Then open `http://<server-ip>:8080/` and log in with the credentials printed on first start (or those set in `config.yaml`).

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
