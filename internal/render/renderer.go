package render

import (
	"image"
	"image/color"

	"github.com/fran150/clementina-video-client/internal/mia"
)

const (
	width  = mia.DisplayWidth
	height = mia.DisplayHeight

	tileSize = 8
	cellsX   = 40
	cellsY   = 25

	localControl = 0x00000
	renderCtl    = 0x00020
	paletteBase  = 0x00100
	chrBase      = 0x00200
	bgNTBase     = 0x0C200
	bgAttrBase   = 0x0E140
	ovNTBase     = 0x10080
	ovAttrBase   = 0x10468
	oamBase      = 0x10850

	regVideoMode      = renderCtl + 0x00
	regLayerEnable    = renderCtl + 0x01
	regBGViewportMode = renderCtl + 0x02
	regBGActiveSet    = renderCtl + 0x03
	regScrollX        = renderCtl + 0x04
	regScrollY        = renderCtl + 0x06
	regBGChrBank      = renderCtl + 0x08
	regBGAltChrBank   = renderCtl + 0x09
	regOverlayChrBank = renderCtl + 0x0A
	regOverlayAltChr  = renderCtl + 0x0B
	regSpriteChrBank  = renderCtl + 0x0C
	regChr1BPPMask    = renderCtl + 0x0D
	regChr1BPPPlanes  = renderCtl + 0x0E
	regBackdropColor  = renderCtl + 0x0F
	regOAMLastIndex   = renderCtl + 0x10

	layerBG      = 1 << 0
	layerOverlay = 1 << 1
	layerSprites = 1 << 2

	attrPal      = 0x0F
	attrFlipX    = 1 << 4
	attrFlipY    = 1 << 5
	attrPriority = 1 << 6
	attrChrAlt   = 1 << 7

	spritePriority = 1 << 4
	spriteFlipX    = 1 << 5
	spriteFlipY    = 1 << 6
	spriteDisable  = 1 << 3
)

type Renderer struct {
	bgColorIndex      [width * height]uint8
	bgPriority        [width * height]bool
	overlayPriority   [width * height]bool
	foregroundBGColor [width * height]color.RGBA
}

func NewRenderer() *Renderer {
	return &Renderer{}
}

func (r *Renderer) Render(vram []byte, target *image.RGBA) {
	if len(vram) < mia.VideoStateSize {
		return
	}

	backdrop := paletteColor(vram, vram[regBackdropColor])
	clearImage(target, backdrop)
	clearMasks(r)

	if vram[regVideoMode]&0x01 == 0 {
		return
	}

	layers := vram[regLayerEnable]
	if layers&layerBG != 0 {
		r.renderBackground(vram, target)
	}
	if layers&layerOverlay != 0 {
		r.precomputeOverlayPriority(vram)
	}
	if layers&layerSprites != 0 {
		r.renderSprites(vram, target)
	}
	if layers&layerBG != 0 {
		r.renderForegroundBackground(target)
	}
	if layers&layerOverlay != 0 {
		r.renderOverlay(vram, target)
	}
}

func clearImage(target *image.RGBA, fill color.RGBA) {
	for y := 0; y < height; y++ {
		row := y * target.Stride
		for x := 0; x < width; x++ {
			i := row + x*4
			target.Pix[i+0] = fill.R
			target.Pix[i+1] = fill.G
			target.Pix[i+2] = fill.B
			target.Pix[i+3] = 0xFF
		}
	}
}

func clearMasks(r *Renderer) {
	clear(r.bgColorIndex[:])
	clear(r.bgPriority[:])
	clear(r.overlayPriority[:])
	clear(r.foregroundBGColor[:])
}

func (r *Renderer) renderBackground(vram []byte, target *image.RGBA) {
	mode := vram[regBGViewportMode]
	activeSet := vram[regBGActiveSet] & 0x01
	scrollX := uint16(vram[regScrollX]) | uint16(vram[regScrollX+1])<<8
	scrollY := uint16(vram[regScrollY]) | uint16(vram[regScrollY+1])<<8

	for y := 0; y < height; y++ {
		worldY := int(scrollY) + y
		coarseY := worldY >> 3
		fineY := worldY & 7
		for x := 0; x < width; x++ {
			worldX := int(scrollX) + x
			coarseX := worldX >> 3
			fineX := worldX & 7
			table, localCell := bgTableAndLocal(mode, activeSet, coarseX, coarseY)
			ntAddr := bgNTBase + table*1000 + localCell
			attrAddr := bgAttrBase + table*1000 + localCell
			tile := vram[ntAddr]
			attr := vram[attrAddr]
			bank := vram[regBGChrBank]
			if attr&attrChrAlt != 0 {
				bank = vram[regBGAltChrBank]
			}
			colorIndex := chrPixel(vram, bank, tile, fineX, fineY, attr, bgPlane(vram))
			pixel := y*width + x
			r.bgColorIndex[pixel] = colorIndex
			c := paletteColorByIndex(vram, attr&attrPal, colorIndex)
			putPixel(target, x, y, c)
			if attr&attrPriority != 0 && colorIndex != 0 {
				r.bgPriority[pixel] = true
				r.foregroundBGColor[pixel] = c
			}
		}
	}
}

func (r *Renderer) precomputeOverlayPriority(vram []byte) {
	for cy := 0; cy < cellsY; cy++ {
		for cx := 0; cx < cellsX; cx++ {
			cell := cy*cellsX + cx
			tile := vram[ovNTBase+cell]
			attr := vram[ovAttrBase+cell]
			if attr&attrPriority == 0 {
				continue
			}
			bank := vram[regOverlayChrBank]
			if attr&attrChrAlt != 0 {
				bank = vram[regOverlayAltChr]
			}
			for py := 0; py < tileSize; py++ {
				for px := 0; px < tileSize; px++ {
					colorIndex := chrPixel(vram, bank, tile, px, py, attr, overlayPlane(vram))
					if colorIndex != 0 {
						x := cx*tileSize + px
						y := cy*tileSize + py
						r.overlayPriority[y*width+x] = true
					}
				}
			}
		}
	}
}

func (r *Renderer) renderSprites(vram []byte, target *image.RGBA) {
	last := int(vram[regOAMLastIndex])
	if last > 255 {
		last = 255
	}

	for i := 0; i <= last; i++ {
		base := oamBase + i*5
		tile := vram[base+0]
		x := signExtend(int(vram[base+1])|int(vram[base+4]&0x03)<<8, 10)
		y := signExtend(int(vram[base+2])|int((vram[base+4]>>2)&0x01)<<8, 9)
		attr := vram[base+3]
		ext := vram[base+4]
		if ext&spriteDisable != 0 {
			continue
		}

		for py := 0; py < tileSize; py++ {
			screenY := y + py
			if screenY < 0 || screenY >= height {
				continue
			}
			for px := 0; px < tileSize; px++ {
				screenX := x + px
				if screenX < 0 || screenX >= width {
					continue
				}
				pixel := screenY*width + screenX
				if r.bgPriority[pixel] || r.overlayPriority[pixel] {
					continue
				}
				if attr&spritePriority != 0 && r.bgColorIndex[pixel] != 0 {
					continue
				}
				colorIndex := chrPixel(vram, vram[regSpriteChrBank], tile, px, py, spriteAttrToCHRAttr(attr), spritePlane(vram))
				if colorIndex == 0 {
					continue
				}
				putPixel(target, screenX, screenY, paletteColorByIndex(vram, attr&attrPal, colorIndex))
			}
		}
	}
}

func (r *Renderer) renderForegroundBackground(target *image.RGBA) {
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			pixel := y*width + x
			if r.bgPriority[pixel] {
				putPixel(target, x, y, r.foregroundBGColor[pixel])
			}
		}
	}
}

func (r *Renderer) renderOverlay(vram []byte, target *image.RGBA) {
	for cy := 0; cy < cellsY; cy++ {
		for cx := 0; cx < cellsX; cx++ {
			cell := cy*cellsX + cx
			tile := vram[ovNTBase+cell]
			attr := vram[ovAttrBase+cell]
			bank := vram[regOverlayChrBank]
			if attr&attrChrAlt != 0 {
				bank = vram[regOverlayAltChr]
			}
			for py := 0; py < tileSize; py++ {
				for px := 0; px < tileSize; px++ {
					colorIndex := chrPixel(vram, bank, tile, px, py, attr, overlayPlane(vram))
					if colorIndex == 0 {
						continue
					}
					putPixel(target, cx*tileSize+px, cy*tileSize+py, paletteColorByIndex(vram, attr&attrPal, colorIndex))
				}
			}
		}
	}
}

func chrPixel(vram []byte, bank uint8, tile uint8, x int, y int, attr uint8, plane int) uint8 {
	if attr&attrFlipX != 0 {
		x = 7 - x
	}
	if attr&attrFlipY != 0 {
		y = 7 - y
	}

	bank &= 0x07
	bankBase := chrBase + int(bank)*6144
	if vram[regChr1BPPMask]&(1<<bank) != 0 {
		if plane < 0 || plane > 2 {
			plane = 0
		}
		row := vram[bankBase+plane*2048+int(tile)*8+y]
		return (row >> x) & 0x01
	}

	p0 := vram[bankBase+0*2048+int(tile)*8+y]
	p1 := vram[bankBase+1*2048+int(tile)*8+y]
	p2 := vram[bankBase+2*2048+int(tile)*8+y]
	return ((p0 >> x) & 1) | (((p1 >> x) & 1) << 1) | (((p2 >> x) & 1) << 2)
}

func bgPlane(vram []byte) int {
	return int(vram[regChr1BPPPlanes] & 0x03)
}

func spritePlane(vram []byte) int {
	return int((vram[regChr1BPPPlanes] >> 2) & 0x03)
}

func overlayPlane(vram []byte) int {
	return int((vram[regChr1BPPPlanes] >> 4) & 0x03)
}

func spriteAttrToCHRAttr(attr uint8) uint8 {
	var out uint8
	if attr&spriteFlipX != 0 {
		out |= attrFlipX
	}
	if attr&spriteFlipY != 0 {
		out |= attrFlipY
	}
	return out
}

func paletteColor(vram []byte, selector uint8) color.RGBA {
	return paletteColorByIndex(vram, (selector>>3)&0x0F, selector&0x07)
}

func paletteColorByIndex(vram []byte, palette uint8, index uint8) color.RGBA {
	addr := paletteBase + int(palette&0x0F)*16 + int(index&0x07)*2
	rgb565 := uint16(vram[addr]) | uint16(vram[addr+1])<<8
	r5 := uint8((rgb565 >> 11) & 0x1F)
	g6 := uint8((rgb565 >> 5) & 0x3F)
	b5 := uint8(rgb565 & 0x1F)
	return color.RGBA{
		R: (r5 << 3) | (r5 >> 2),
		G: (g6 << 2) | (g6 >> 4),
		B: (b5 << 3) | (b5 >> 2),
		A: 0xFF,
	}
}

func putPixel(target *image.RGBA, x int, y int, c color.RGBA) {
	i := y*target.Stride + x*4
	target.Pix[i+0] = c.R
	target.Pix[i+1] = c.G
	target.Pix[i+2] = c.B
	target.Pix[i+3] = 0xFF
}

func bgTableAndLocal(mode uint8, activeSet uint8, coarseX int, coarseY int) (int, int) {
	var planeCols, planeRows int
	var tableForCell func(int, int) int

	switch mode {
	case 1:
		planeCols, planeRows = 80, 25
		tableForCell = func(x, y int) int { return x / 40 }
	case 2:
		planeCols, planeRows = 40, 50
		tableForCell = func(x, y int) int {
			if y >= 25 {
				return 2
			}
			return 0
		}
	case 3:
		planeCols, planeRows = 160, 25
		tableForCell = func(x, y int) int { return x / 40 }
	case 4:
		planeCols, planeRows = 40, 100
		tableForCell = func(x, y int) int { return y / 25 }
	case 5:
		planeCols, planeRows = 80, 50
		tableForCell = func(x, y int) int {
			return (y/25)*2 + (x / 40)
		}
	default:
		planeCols, planeRows = 40, 25
		tableForCell = func(x, y int) int { return 0 }
	}

	x := positiveMod(coarseX, planeCols)
	y := positiveMod(coarseY, planeRows)
	table := tableForCell(x, y) + int(activeSet&0x01)*4
	localX := x % 40
	localY := y % 25
	return table, localY*40 + localX
}

func positiveMod(value int, divisor int) int {
	out := value % divisor
	if out < 0 {
		out += divisor
	}
	return out
}

func signExtend(value int, bits int) int {
	sign := 1 << (bits - 1)
	mask := (1 << bits) - 1
	value &= mask
	if value&sign != 0 {
		value -= 1 << bits
	}
	return value
}
