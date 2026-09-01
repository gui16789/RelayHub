# Security Policy

## Reporting a Vulnerability

This proxy handles API keys and forwards traffic to upstream LLM providers.
If you discover a security vulnerability, **do not open a public GitHub issue**.

Please report it privately by one of the following channels:

- **GitHub Security Advisory** — click "Report a vulnerability" on the
  repository's Security tab (preferred).
- **Email** — send a detailed report to the repository maintainer.

Please include:

1. A clear description of the vulnerability
2. Steps to reproduce
3. Affected version(s)
4. Any suggested mitigations

## Supported Versions

| Version | Supported |
|---------|-----------|
| v1.x    | ✅        |

## Security Design Notes

- The admin console is **loopback-only by default** (`127.0.0.1` / `::1`).
  Remote access requires `server.admin_key` in `config.yaml`.
- API keys use **constant-time comparison** to prevent timing attacks.
- Config and stats files are written with `0600` permissions.
- Logs and errors only expose the **last 4 characters** of API keys.
- Per-channel header overrides cannot set `Authorization` or `Content-Type`.
- Config writes use a temp-file + atomic rename so a crash cannot corrupt
  the config.
- Desktop log files are stored in `%APPDATA%\RelayHub\logs\` by default.
