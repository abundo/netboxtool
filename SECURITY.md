# Security Policy

## Supported Versions

This project does not maintain release branches; only the latest commit on
`main` is supported.

## Reporting a Vulnerability

Please report security issues privately by emailing anders@abundo.se rather
than opening a public GitHub issue. Include reproduction steps and the
Netbox/netboxtool versions involved. You should receive a response within a
few days.

## Known considerations

- **API token storage** — the CLI reads a Netbox API token from a config
  file (`netbox.token` in `netboxtool.yaml`, see [README.md](README.md#configuration)).
  This token carries whatever permissions it's been granted in Netbox, so
  the config file should be readable only by the user running `netboxtool`
  (e.g. `chmod 600`).
- **TLS verification** — TLS certificate verification is enabled by
  default. It can be disabled via `netbox.insecure: true` in the config
  file for testing against a Netbox instance with a self-signed or
  otherwise unverifiable certificate; this should not be used against any
  Netbox server reachable over an untrusted network, since it removes
  protection against MITM attacks on both the connection and the API
  token sent with every request.
