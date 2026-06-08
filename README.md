# Clementina Video Client

Native UDP video client for Clementina MIA video output.

The client keeps a local mirror of MIA video RAM, requests dirty-page updates
over UDP, repairs missing chunks with `NACK_CHUNKS`, acknowledges completed
responses, and renders the mirrored tile/sprite state to a 320x200 display.

## Run

```bash
go run ./cmd/clementina-video-client
```

Useful flags:

```bash
go run ./cmd/clementina-video-client --server 127.0.0.1:6502
go run ./cmd/clementina-video-client --server 192.168.1.50:6502 --fullscreen
go run ./cmd/clementina-video-client --fps 30 --scale 5
```

Defaults:

| Flag | Default | Meaning |
| --- | ---: | --- |
| `--server` | `127.0.0.1:6502` | MIA UDP endpoint |
| `--bind` | `:0` | local UDP bind address |
| `--fps` | `25` | request cadence |
| `--repair-timeout` | `100ms` | quiet time before retry/NACK |
| `--no-response-retries` | `3` | retries before a new HELLO |
| `--scale` | `4` | initial window scale |
| `--fullscreen` | `false` | start fullscreen |
| `--debug-overlay` | `true` | show connection/FPS/protocol HUD |

Keys:

- `F3`: toggle debug overlay
- `F11`: toggle fullscreen
- `Escape`: quit
