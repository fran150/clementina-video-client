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
| `--input-retry-interval` | `1s` | input HELLO cadence while unconnected |
| `--disable-keyboard` | `false` | do not forward keyboard input |
| `--disable-mouse` | `false` | do not forward mouse input |
| `--disable-gamepads` | `false` | do not forward gamepad input |
| `--fps` | `25` | request cadence |
| `--repair-timeout` | `100ms` | quiet time before retry/NACK |
| `--no-response-retries` | `3` | retries before a new HELLO |
| `--scale` | `4` | initial window scale |
| `--fullscreen` | `false` | start fullscreen |
| `--debug-overlay` | `false` | show connection/FPS/protocol HUD |
| `--paste-rate` | `40` | clipboard paste rate, in characters per second |

Keys:

- `Ctrl+M`: toggle normal/control mode
- `Ctrl+V` / `Cmd+V`: paste the system clipboard, sent to MIA as `TEXT` a few
  characters at a time (see [Clipboard paste](#clipboard-paste))
- `F1`: return to normal mode from control mode
- `F3`: toggle debug overlay in control mode
- `F5`: capture/release mouse in control mode
- `F6`: clear MIA input state in control mode
- `F11`: toggle fullscreen in control mode
- `F12`: force an immediate input reconnect in control mode
- `Escape`: quit in control mode

## Clipboard paste

`Ctrl+V` (`Cmd+V` on macOS) reads the system clipboard and queues it to be
sent to MIA as `TEXT` packets, as if it were typed. It never reaches the guest
as a literal keystroke: that chord is reserved for paste.

The queue drains at `--paste-rate` characters per second (default `40`)
instead of being sent in one burst. MIA's text FIFO is only 64 bytes and has
no flow control back to the client (see the `TEXT` packet in MIA's Wi-Fi input
protocol docs), so a large paste sent all at once can silently lose its oldest
bytes if the guest program isn't reading fast enough; pacing it keeps the FIFO
close to what a real typist would produce. Losing window focus (or `F6`) drops
any paste still in flight instead of resuming it later.

Only printable ASCII is sent; other characters are dropped. Tab, `\n`, and
`\r`/`\r\n` are mapped to the same text byte their key would send (so a pasted
line break behaves like pressing Enter).

## Input reconnection

MIA sends nothing when the computer or MIA itself resets: both the video and the
input session are wiped, and packets carrying the old session id are silently
dropped. The client recovers on its own, so `F12` is only needed to skip the
wait:

- while the input client has no session it re-sends `HELLO` every
  `--input-retry-interval`, which also covers starting before MIA is reachable;
- every fresh video session triggers an input `HELLO`, since a reset drops both
  sessions together and the video client already re-announces itself after
  `--no-response-retries` unanswered requests.

Normal mode forwards keyboard and gamepad input to MIA. Mouse input is forwarded
only while the mouse is captured. Control mode shows input/video status and
reserves the keys above for the client; other keyboard and gamepad input is
still forwarded.
