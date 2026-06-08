package render

import (
	"image"
	"testing"

	"github.com/fran150/clementina-video-client/internal/mia"
)

func TestBackgroundMode5WrapsAcrossActiveSet(t *testing.T) {
	table, local := bgTableAndLocal(5, 1, 81, 51)

	if table != 4 {
		t.Fatalf("table = %d, want 4", table)
	}
	if local != 41 {
		t.Fatalf("local = %d, want 41", local)
	}
}

func TestRendererDrawsBackdropWhenVideoEnabled(t *testing.T) {
	vram := make([]byte, mia.VideoStateSize)
	vram[regVideoMode] = 1
	vram[regBackdropColor] = 0
	vram[paletteBase+0] = 0xE0
	vram[paletteBase+1] = 0x07

	target := image.NewRGBA(image.Rect(0, 0, mia.DisplayWidth, mia.DisplayHeight))
	NewRenderer().Render(vram, target)

	want := []uint8{0, 255, 0, 255}
	for i, value := range want {
		if target.Pix[i] != value {
			t.Fatalf("pixel byte %d = %d, want %d", i, target.Pix[i], value)
		}
	}
}
