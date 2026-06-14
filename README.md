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
go run ./cmd/clementina-video-client --disable-mouse --disable-gamepads
```

Defaults:

| Flag | Default | Meaning |
| --- | ---: | --- |
| `--server` | `127.0.0.1:6502` | MIA UDP endpoint |
| `--bind` | `:0` | local UDP bind address |
| `--input` | `true` | enable MIA Wi-Fi input |
| `--input-server` | server host, port `6503` | MIA input UDP endpoint |
| `--input-bind` | `:0` | local input UDP bind address |
| `--disable-keyboard` | `false` | do not forward keyboard input |
| `--disable-mouse` | `false` | do not forward mouse input |
| `--disable-gamepads` | `false` | do not forward gamepad input |
| `--fps` | `25` | request cadence |
| `--repair-timeout` | `100ms` | quiet time before retry/NACK |
| `--no-response-retries` | `3` | retries before a new HELLO |
| `--scale` | `4` | initial window scale |
| `--fullscreen` | `false` | start fullscreen |
| `--debug-overlay` | `false` | show connection/FPS/protocol HUD |

Keys:

- `Ctrl+M`: toggle normal/control mode
- `F1`: return to normal mode from control mode
- `F3`: toggle debug overlay in control mode
- `F5`: capture/release mouse in control mode
- `F6`: clear MIA input state in control mode
- `F11`: toggle fullscreen in control mode
- `F12`: attempt input reconnect in control mode
- `Escape`: quit in control mode

Normal mode forwards keyboard and gamepad input to MIA. Mouse input is forwarded
only while the mouse is captured. Control mode shows input/video status and
reserves the keys above for the client; other keyboard and gamepad input is
still forwarded.
