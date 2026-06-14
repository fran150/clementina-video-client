package main

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
	"strings"
	"time"

	"github.com/fran150/clementina-video-client/internal/config"
	clientinput "github.com/fran150/clementina-video-client/internal/input"
	"github.com/fran150/clementina-video-client/internal/mia"
	"github.com/fran150/clementina-video-client/internal/render"
	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
)

var errQuit = errors.New("quit")

type clientMode int

const (
	modeNormal clientMode = iota
	modeControl
)

type Game struct {
	client         *mia.Client
	inputClient    *mia.InputClient
	inputForwarder *clientinput.Forwarder
	renderer       *render.Renderer
	frame          *image.RGBA
	texture        *ebiten.Image
	snapshot       []byte

	showOverlay        bool
	mode               clientMode
	mouseCaptured      bool
	wasFocused         bool
	suppressModeChord  bool
	serverAddress      string
	inputServerAddress string
	requestFPS         int
	repairTimeout      time.Duration
	keyboardEnabled    bool
	mouseEnabled       bool
	gamepadsEnabled    bool
	protocolFPS        float64
	lastProtocolSample time.Time
	lastAppliedFrames  uint64
}

func NewGame(client *mia.Client, inputClient *mia.InputClient, cfg config.Config) *Game {
	keyboardEnabled := cfg.InputEnabled && !cfg.DisableKeyboard
	mouseEnabled := cfg.InputEnabled && !cfg.DisableMouse
	gamepadsEnabled := cfg.InputEnabled && !cfg.DisableGamepads
	mode := modeNormal
	if cfg.DebugOverlay {
		mode = modeControl
	}

	return &Game{
		client:             client,
		inputClient:        inputClient,
		inputForwarder:     clientinput.NewForwarder(inputClient, clientinput.Config{Keyboard: keyboardEnabled, Mouse: mouseEnabled, Gamepads: gamepadsEnabled}),
		renderer:           render.NewRenderer(),
		frame:              image.NewRGBA(image.Rect(0, 0, mia.DisplayWidth, mia.DisplayHeight)),
		texture:            ebiten.NewImage(mia.DisplayWidth, mia.DisplayHeight),
		snapshot:           make([]byte, mia.VideoStateSize),
		showOverlay:        cfg.DebugOverlay,
		mode:               mode,
		wasFocused:         true,
		serverAddress:      cfg.ServerAddress,
		inputServerAddress: cfg.InputServerAddress,
		requestFPS:         cfg.RequestFPS,
		repairTimeout:      cfg.RepairTimeout,
		keyboardEnabled:    keyboardEnabled,
		mouseEnabled:       mouseEnabled,
		gamepadsEnabled:    gamepadsEnabled,
		lastProtocolSample: time.Now(),
	}
}

func (g *Game) Update() error {
	focused := ebiten.IsFocused()
	if !focused {
		if g.wasFocused {
			g.inputForwarder.ClearAll()
			g.setMouseCaptured(false)
		}
		g.wasFocused = false
		return nil
	}
	if !g.wasFocused {
		g.inputForwarder.Sync()
		g.wasFocused = true
	}

	g.suppressModeChord = g.modeChordJustPressed()
	if g.suppressModeChord {
		g.toggleMode()
		g.inputForwarder.ClearKeyboard()
	}

	if g.mode == modeControl {
		if inpututil.IsKeyJustPressed(ebiten.KeyEscape) {
			return errQuit
		}
		if inpututil.IsKeyJustPressed(ebiten.KeyF1) {
			g.enterNormalMode()
		}
		if inpututil.IsKeyJustPressed(ebiten.KeyF3) {
			g.showOverlay = !g.showOverlay
		}
		if inpututil.IsKeyJustPressed(ebiten.KeyF5) {
			g.setMouseCaptured(!g.mouseCaptured)
		}
		if inpututil.IsKeyJustPressed(ebiten.KeyF6) {
			g.inputForwarder.ClearAll()
		}
		if inpututil.IsKeyJustPressed(ebiten.KeyF11) {
			ebiten.SetFullscreen(!ebiten.IsFullscreen())
		}
		if inpututil.IsKeyJustPressed(ebiten.KeyF12) {
			g.inputClient.RequestConnect()
		}
	}

	g.inputForwarder.Update(g.keyBlocked, g.mouseCaptured)
	return nil
}

func (g *Game) Draw(screen *ebiten.Image) {
	g.snapshot = g.client.Snapshot(g.snapshot)
	g.renderer.Render(g.snapshot, g.frame)
	g.texture.WritePixels(g.frame.Pix)

	screenW, screenH := screen.Size()
	scale := math.Min(float64(screenW)/float64(mia.DisplayWidth), float64(screenH)/float64(mia.DisplayHeight))
	if scale <= 0 {
		scale = 1
	}

	drawW := float64(mia.DisplayWidth) * scale
	drawH := float64(mia.DisplayHeight) * scale
	opts := &ebiten.DrawImageOptions{}
	opts.Filter = ebiten.FilterNearest
	opts.GeoM.Scale(scale, scale)
	opts.GeoM.Translate((float64(screenW)-drawW)/2, (float64(screenH)-drawH)/2)
	screen.DrawImage(g.texture, opts)

	if g.showOverlay {
		g.drawOverlay(screen)
	}
}

func (g *Game) Layout(outsideWidth int, outsideHeight int) (int, int) {
	return outsideWidth, outsideHeight
}

func (g *Game) drawOverlay(screen *ebiten.Image) {
	stats := g.client.Stats()
	inputStats := g.inputClient.Stats()
	g.updateProtocolFPS(stats)

	connected := "no"
	if stats.SessionID != 0 {
		connected = "yes"
	}

	inputSession := "--------"
	if inputStats.SessionID != 0 {
		inputSession = fmt.Sprintf("%08X", inputStats.SessionID)
	}

	lines := []string{
		fmt.Sprintf("Clementina Video  connected: %s", connected),
		fmt.Sprintf("server: %s", g.serverAddress),
		fmt.Sprintf("render fps: %.1f   request fps: %d   apply fps: %.1f", ebiten.ActualFPS(), g.requestFPS, g.protocolFPS),
		fmt.Sprintf("session: %08X   frame: %d   request: %d", stats.SessionID, stats.LastFrameID, stats.RequestID),
		fmt.Sprintf("dirty pages: %d   repairs: %d   no-dirty: %d   reconnects: %d", stats.LastDirtyPages, stats.RepairsSent, stats.NoDirtyResponses, stats.Reconnects),
		fmt.Sprintf("repair timeout: %s   status: %s", g.repairTimeout, stats.LastStatus),
		fmt.Sprintf("input: %s   server: %s   session: %s   sent: %d", inputStats.State, g.inputServerAddress, inputSession, inputStats.SentPackets),
		fmt.Sprintf("mode: %s   mouse: %s   keyboard:%s mouse:%s gamepads:%s", g.modeName(), boolStatus(g.mouseCaptured), boolStatus(g.keyboardEnabled), boolStatus(g.mouseEnabled), boolStatus(g.gamepadsEnabled)),
		"Ctrl+M mode   F1 normal   F3 overlay   F5 mouse   F6 clear   F11 fullscreen   F12 reconnect   Esc quit",
	}
	if inputStats.LastError != "" {
		lines = append(lines, "input error: "+inputStats.LastError)
	}

	width := 760.0
	height := float64(len(lines)*16 + 12)
	ebitenutil.DrawRect(screen, 8, 8, width, height, color.RGBA{0, 0, 0, 190})
	ebitenutil.DebugPrintAt(screen, strings.Join(lines, "\n"), 14, 14)
}

func (g *Game) updateProtocolFPS(stats mia.Stats) {
	now := time.Now()
	elapsed := now.Sub(g.lastProtocolSample)
	if elapsed < 500*time.Millisecond {
		return
	}

	frames := stats.AppliedFrames - g.lastAppliedFrames
	g.protocolFPS = float64(frames) / elapsed.Seconds()
	g.lastProtocolSample = now
	g.lastAppliedFrames = stats.AppliedFrames
}

func (g *Game) modeChordJustPressed() bool {
	return inpututil.IsKeyJustPressed(ebiten.KeyM) && controlPressed()
}

func (g *Game) keyBlocked(key ebiten.Key) bool {
	if g.suppressModeChord && key == ebiten.KeyM {
		return true
	}
	if g.mode != modeControl {
		return false
	}
	switch key {
	case ebiten.KeyEscape, ebiten.KeyF1, ebiten.KeyF3, ebiten.KeyF5, ebiten.KeyF6, ebiten.KeyF11, ebiten.KeyF12:
		return true
	case ebiten.KeyM:
		return controlPressed()
	default:
		return false
	}
}

func (g *Game) toggleMode() {
	if g.mode == modeControl {
		g.enterNormalMode()
		return
	}
	g.enterControlMode()
}

func (g *Game) enterControlMode() {
	g.mode = modeControl
	g.showOverlay = true
}

func (g *Game) enterNormalMode() {
	g.mode = modeNormal
	g.showOverlay = false
}

func (g *Game) modeName() string {
	if g.mode == modeControl {
		return "control"
	}
	return "normal"
}

func (g *Game) setMouseCaptured(captured bool) {
	if g.mouseCaptured == captured {
		return
	}
	g.mouseCaptured = captured
	if captured {
		ebiten.SetCursorMode(ebiten.CursorModeCaptured)
		return
	}
	ebiten.SetCursorMode(ebiten.CursorModeVisible)
	g.inputForwarder.ClearMouse()
}

func controlPressed() bool {
	return ebiten.IsKeyPressed(ebiten.KeyControlLeft) ||
		ebiten.IsKeyPressed(ebiten.KeyControlRight) ||
		ebiten.IsKeyPressed(ebiten.KeyControl)
}

func boolStatus(value bool) string {
	if value {
		return "on"
	}
	return "off"
}

func main() {
	cfg, err := config.Parse(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "configuration error: %v\n", err)
		os.Exit(2)
	}

	client := mia.NewClient(mia.ClientConfig{
		ServerAddress:     cfg.ServerAddress,
		BindAddress:       cfg.BindAddress,
		RequestFPS:        cfg.RequestFPS,
		RepairTimeout:     cfg.RepairTimeout,
		NoResponseRetries: cfg.NoResponseRetries,
		LogFrames:         cfg.LogFrames,
	})
	if err := client.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "video client error: %v\n", err)
		os.Exit(1)
	}
	defer client.Close()

	inputClient := mia.NewInputClient(mia.InputClientConfig{
		Enabled:       cfg.InputEnabled && (!cfg.DisableKeyboard || !cfg.DisableMouse || !cfg.DisableGamepads),
		ServerAddress: cfg.InputServerAddress,
		BindAddress:   cfg.InputBindAddress,
		Capabilities:  inputCapabilities(cfg),
	})
	if err := inputClient.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "input client error: %v\n", err)
		os.Exit(1)
	}
	defer inputClient.Close()

	ebiten.SetWindowTitle(cfg.Title)
	ebiten.SetWindowSize(mia.DisplayWidth*cfg.Scale, mia.DisplayHeight*cfg.Scale)
	ebiten.SetWindowResizingMode(ebiten.WindowResizingModeEnabled)
	ebiten.SetFullscreen(cfg.Fullscreen)
	ebiten.SetVsyncEnabled(true)

	if err := ebiten.RunGame(NewGame(client, inputClient, cfg)); err != nil && !errors.Is(err, errQuit) {
		fmt.Fprintf(os.Stderr, "render error: %v\n", err)
		os.Exit(1)
	}
}

func inputCapabilities(cfg config.Config) uint16 {
	var caps uint16
	if cfg.InputEnabled && !cfg.DisableKeyboard {
		caps |= mia.InputCapText | mia.InputCapKeyboard | mia.InputCapConsumer
	}
	if cfg.InputEnabled && !cfg.DisableMouse {
		caps |= mia.InputCapMouse
	}
	if cfg.InputEnabled && !cfg.DisableGamepads {
		caps |= mia.InputCapGamepad
	}
	return caps
}
