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
	"github.com/fran150/clementina-video-client/internal/mia"
	"github.com/fran150/clementina-video-client/internal/render"
	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
)

var errQuit = errors.New("quit")

type Game struct {
	client   *mia.Client
	renderer *render.Renderer
	frame    *image.RGBA
	texture  *ebiten.Image
	snapshot []byte

	showOverlay        bool
	serverAddress      string
	requestFPS         int
	repairTimeout      time.Duration
	protocolFPS        float64
	lastProtocolSample time.Time
	lastAppliedFrames  uint64
}

func NewGame(client *mia.Client, cfg config.Config) *Game {
	return &Game{
		client:             client,
		renderer:           render.NewRenderer(),
		frame:              image.NewRGBA(image.Rect(0, 0, mia.DisplayWidth, mia.DisplayHeight)),
		texture:            ebiten.NewImage(mia.DisplayWidth, mia.DisplayHeight),
		snapshot:           make([]byte, mia.VideoStateSize),
		showOverlay:        cfg.DebugOverlay,
		serverAddress:      cfg.ServerAddress,
		requestFPS:         cfg.RequestFPS,
		repairTimeout:      cfg.RepairTimeout,
		lastProtocolSample: time.Now(),
	}
}

func (g *Game) Update() error {
	if inpututil.IsKeyJustPressed(ebiten.KeyEscape) {
		return errQuit
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyF11) {
		ebiten.SetFullscreen(!ebiten.IsFullscreen())
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyF3) {
		g.showOverlay = !g.showOverlay
	}

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
	g.updateProtocolFPS(stats)

	connected := "no"
	if stats.SessionID != 0 {
		connected = "yes"
	}

	lines := []string{
		fmt.Sprintf("Clementina Video  connected: %s", connected),
		fmt.Sprintf("server: %s", g.serverAddress),
		fmt.Sprintf("render fps: %.1f   request fps: %d   apply fps: %.1f", ebiten.ActualFPS(), g.requestFPS, g.protocolFPS),
		fmt.Sprintf("session: %08X   frame: %d   request: %d", stats.SessionID, stats.LastFrameID, stats.RequestID),
		fmt.Sprintf("dirty pages: %d   repairs: %d   no-dirty: %d   reconnects: %d", stats.LastDirtyPages, stats.RepairsSent, stats.NoDirtyResponses, stats.Reconnects),
		fmt.Sprintf("repair timeout: %s   status: %s", g.repairTimeout, stats.LastStatus),
		"F3 overlay   F11 fullscreen   Esc quit",
	}

	width := 500.0
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

	ebiten.SetWindowTitle(cfg.Title)
	ebiten.SetWindowSize(mia.DisplayWidth*cfg.Scale, mia.DisplayHeight*cfg.Scale)
	ebiten.SetWindowResizingMode(ebiten.WindowResizingModeEnabled)
	ebiten.SetFullscreen(cfg.Fullscreen)
	ebiten.SetVsyncEnabled(true)

	if err := ebiten.RunGame(NewGame(client, cfg)); err != nil && !errors.Is(err, errQuit) {
		fmt.Fprintf(os.Stderr, "render error: %v\n", err)
		os.Exit(1)
	}
}
