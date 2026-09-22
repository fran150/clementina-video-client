// Package render exposes the video client's compositor for headless tools.
package render

import (
	"fmt"
	"github.com/fran150/clementina-video-client/internal/mia"
	internal "github.com/fran150/clementina-video-client/internal/render"
	"image"
)

// Snapshot renders a native-resolution image. The input must be a coherent MIA snapshot.
func Snapshot(vram []byte) (*image.RGBA, error) {
	if len(vram) != mia.VideoStateSize {
		return nil, fmt.Errorf("expected %d video bytes", mia.VideoStateSize)
	}
	target := image.NewRGBA(image.Rect(0, 0, mia.DisplayWidth, mia.DisplayHeight))
	internal.NewRenderer().Render(vram, target)
	return target, nil
}
