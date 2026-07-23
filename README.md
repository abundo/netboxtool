# Netboxtool

A Go library and CLI for reading (and partially writing) data in
[Netbox](https://netboxlabs.com/) — devices, virtual machines, device types,
manufacturers and their interfaces/IP addresses — via Netbox's GraphQL and
REST APIs.

- `github.com/abundo/netboxtool` — the library (`netboxtool.go`,
  `netboxtool_graphql.go`, `models.go`)
- `cmd/netboxtool_cli.go` — the `netboxtool` CLI, built on
  [cobra](https://github.com/spf13/cobra) and
  [boa](https://github.com/GiGurra/boa)

## Configuration

The CLI reads a YAML config file, by default `/etc/netboxtool/netboxtool.yaml`
(override with `--configfile`):

```yaml
netbox:
  url: https://netbox.example.com
  token: <netbox API token>
  insecure: false # set true to skip TLS certificate verification
```

## Usage

```
netboxtool show-config
netboxtool get-device      --name <name> | --id <id>
netboxtool get-devices
netboxtool get-vm          --name <name> | --id <id>
netboxtool get-vms
netboxtool get-device-type --manufacturer <name> --model <model>
netboxtool get-manufacturer --name <name> | --id <id>
```

Run `netboxtool <command> -h` for a command's full flag list, or
`netboxtool -h` for the top-level list. Global flags (`--configfile`,
`--debug`, `--loglevel`) are available on every command.

## Build

Assumes the code is checked out under `~/code/netboxtool`:

    cd code
    git clone https://github.com/abundo/netboxtool
    cd netboxtool
    go mod tidy
    make

Binary is written to `build/netboxtool`.

## Install

Default install location is `/usr/bin`:

    sudo make install

## Development

See [DEV.md](DEV.md) for internal architecture notes and lessons learned
from past performance work.
