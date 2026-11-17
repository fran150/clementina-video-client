# Clementina Video Client

Native UDP video client for Clementina MIA video output.

The client keeps a local mirror of MIA video RAM, requests dirty-page updates
over UDP, repairs missing chunks with `NACK_CHUNKS`, acknowledges completed
responses, and renders the mirrored tile/sprite state to a 320x200 display.

## Install

The [releases](https://github.com/fran150/clementina-video-client/releases)
attach one archive per platform (`linux-amd64`, `linux-arm64`, `darwin-arm64`,
`darwin-amd64`, `windows-amd64`) and a `SHA256SUMS` file. Each archive holds
`clementina-video-client` and `clementina-render`, which turns a snapshot of
MIA's video state into a PNG on stdout: `clementina-render debug.video.bin >
frame.png` reads the raw bytes from a file, and with no file it reads a JSON
array of them on stdin. On Linux the client needs
the X11 and OpenGL runtime libraries, which desktop distributions already
have. macOS may quarantine a downloaded executable;
`xattr -d com.apple.quarantine` clears that.

### Dev install

`make dev-install` builds `clementina-video-client` and `clementina-render` into
`~/.clementina/dev/bin` (or `$CLEMENTINA_DEV_BIN`), the folder that the
Clementina SDK's `clementina dev on` switches every terminal to; see the SDK's [dev installs](https://github.com/fran150/clementina-sdk/blob/main/docs/dev-install.md).
`--version` prints `dev` and the commit for such a build, and the release tag
for a downloaded one.

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
| `--repair-timeout` | `100ms` | longest quiet time before retry/NACK (see below) |
| `--playout-delay` | `0` (off) | hold each frame this long before showing it (see below) |
| `--no-response-retries` | `10` | a new HELLO after this many `--repair-timeout`s with no reply |
| `--scale` | `4` | initial window scale |
| `--fullscreen` | `false` | start fullscreen |
| `--debug-overlay` | `false` | show connection/FPS/protocol HUD |
| `--paste-rate` | `40` | clipboard paste rate, in characters per second |

Keys:

- `Ctrl+M`: toggle normal/control mode
- `Ctrl+I`: sent to the guest as Insert (`$94`) — a stand-in for the physical
  Insert key Mac keyboards don't have
- `Ctrl+V` / `Cmd+V`: paste the system clipboard, sent to MIA as `TEXT` a few
  characters at a time (see [Clipboard paste](#clipboard-paste))
- `F1`: return to normal mode from control mode
- `F3`: toggle debug overlay in control mode
- `F5`: capture/release mouse in control mode
- `F6`: clear MIA input state in control mode
- `F11`: toggle fullscreen in control mode
- `F12`: force an immediate input reconnect in control mode
- `Escape`: quit in control mode

Every other key goes to the guest. The byte each one types, and which keys MIA
decodes itself, are in the key code table in MIA's
[docs/input.md](https://github.com/fran150/clementina-mia/blob/main/docs/input.md#key-codes).

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

## Repair timeout

The client measures the round trip of each request, from sending it to MIA's
first reply, and sets the quiet time before it re-sends a request or NACKs
missing chunks the way TCP sets its retransmission timeout: the smoothed round
trip plus four times its variation, at least 40 ms (one frame at 25 fps) and at most
`--repair-timeout`. On a quick, steady link a lost packet is repaired after
40 ms; on a jittery one the timeout grows, so packets that are
only late are not asked for again. Unanswered request retries double the wait
each time, up to `--repair-timeout`. The client reconnects with a new `HELLO`
when a request has had no reply for `--no-response-retries` x
`--repair-timeout` (1 s by default). The overlay (`F3`) shows the current
timeout and the smoothed round trip.

## Playout buffer

Wi-Fi delivers MIA's replies unevenly: most arrive within a few milliseconds,
some much later. The client asks for frames on a fixed cadence, so with
`--playout-delay 60ms` it shows each frame 60 ms after asking for it instead of
the moment it arrives. Replies that are up to that late still appear on time,
so motion stays even, at the cost of that much extra lag on the picture and on
seeing your own key presses. A frame that arrives after its slot is shown at
once and counted as `late` on the overlay (`F3`), which also shows the delay,
the frames waiting and the frames shown. The buffer cannot hide a stall longer
than the delay, and it cannot bring back motion MIA never sent: while the link
is stalled MIA keeps collecting changes, and they arrive as one bigger frame.
Values between `40ms` and `80ms` are a good start; it is off by default.

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
