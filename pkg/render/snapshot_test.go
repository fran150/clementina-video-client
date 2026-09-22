package render

import (
	"image/color"
	"testing"
)

func TestSnapshot(t *testing.T) {
	if _, err := Snapshot(nil); err == nil {
		t.Fatal("accepted missing state")
	}
	state := make([]byte, 68944)
	state[256] = 0xe0
	state[257] = 7
	img, err := Snapshot(state)
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != 320 || img.Bounds().Dy() != 200 {
		t.Fatal(img.Bounds())
	}
	if img.RGBAAt(0, 0) != (color.RGBA{0, 255, 0, 255}) {
		t.Fatal(img.RGBAAt(0, 0))
	}
}
