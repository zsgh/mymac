// Command genicon renders mymac's application icon into web/assets.
//
// Run with: go run ./tools/genicon
//
// It writes favicon.ico (16/32/48 PNG frames), favicon.svg and
// apple-touch-icon.png so the assets stay reproducible instead of being
// hand-drawn binaries.
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

const (
	outDir  = "web/assets"
	master  = 1024
	iconPNG = 180
)

func main() {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fail(err)
	}

	// favicon.svg
	svg := renderSVG()
	if err := os.WriteFile(filepath.Join(outDir, "favicon.svg"), []byte(svg), 0o644); err != nil {
		fail(err)
	}

	// apple-touch-icon.png (opaque, no rounded corners - the OS masks it)
	touch := render(master, false)
	if err := writePNG(filepath.Join(outDir, "apple-touch-icon.png"), touch, iconPNG); err != nil {
		fail(err)
	}

	// favicon.ico with 16/32/48 frames
	var frames [][]byte
	for _, size := range []int{16, 32, 48} {
		img := render(size, true)
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			fail(err)
		}
		frames = append(frames, buf.Bytes())
	}
	if err := os.WriteFile(filepath.Join(outDir, "favicon.ico"), buildICO(frames), 0o644); err != nil {
		fail(err)
	}

	// A large PNG is handy for the app bundle later.
	if err := writePNG(filepath.Join(outDir, "icon-512.png"), render(512, true), 512); err != nil {
		fail(err)
	}

	fmt.Println("icons written to", outDir)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "genicon:", err)
	os.Exit(1)
}

// buildICO packs PNG frames into a Windows icon container.
func buildICO(frames [][]byte) []byte {
	var buf bytes.Buffer
	binary.Write(&buf, binary.LittleEndian, uint16(0)) // reserved
	binary.Write(&buf, binary.LittleEndian, uint16(1)) // type: icon
	binary.Write(&buf, binary.LittleEndian, uint16(len(frames)))

	offset := 6 + 16*len(frames)
	for i, f := range frames {
		size := byte(len(frames[i+0][0:0])) // placeholder, replaced below
		_ = size
		binary.Write(&buf, binary.LittleEndian, uint8(0))   // width placeholder
		binary.Write(&buf, binary.LittleEndian, uint8(0))   // height placeholder
		binary.Write(&buf, binary.LittleEndian, uint8(0))   // palette
		binary.Write(&buf, binary.LittleEndian, uint8(0))   // reserved
		binary.Write(&buf, binary.LittleEndian, uint16(1))  // color planes
		binary.Write(&buf, binary.LittleEndian, uint16(32)) // bits per pixel
		binary.Write(&buf, binary.LittleEndian, uint32(len(f)))
		binary.Write(&buf, binary.LittleEndian, uint32(offset))
		offset += len(f)
	}
	// Patch the width/height bytes (0 means 256, we only use <=48).
	dims := []byte{16, 32, 48}
	for i, d := range dims {
		pos := 6 + 16*i
		buf.Bytes()[pos] = d
		buf.Bytes()[pos+1] = d
	}
	for _, f := range frames {
		buf.Write(f)
	}
	return buf.Bytes()
}

func writePNG(path string, img image.Image, size int) error {
	out := image.NewRGBA(image.Rect(0, 0, size, size))
	draw.Draw(out, out.Bounds(), img, img.Bounds().Min, draw.Src)
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, out)
}

// render draws the icon: a rounded gradient tile with a terminal prompt.
func render(size int, rounded bool) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	draw.Draw(img, img.Bounds(), image.Transparent, image.Point{}, draw.Src)

	s := float64(size)
	radius := s * 0.2237 // macOS Big Sur corner radius ratio
	inset := s * 0.055
	plate := image.Rect(int(inset), int(inset), int(s-inset), int(s-inset))

	c1 := color.NRGBA{0x0A, 0x84, 0xFF, 0xFF} // system blue
	c2 := color.NRGBA{0x64, 0x52, 0xE0, 0xFF} // indigo
	edge := color.NRGBA{0x30, 0x30, 0x38, 0xFF}

	// Base plate.
	for y := plate.Min.Y; y < plate.Max.Y; y++ {
		for x := plate.Min.X; x < plate.Max.X; x++ {
			fx, fy := float64(x)+0.5, float64(y)+0.5
			if rounded && !insideRounded(fx, fy, plate, radius) {
				continue
			}
			// Diagonal gradient.
			t := clamp01((fx + fy - 2*float64(plate.Min.Y)) / (2 * s))
			c := lerp(c1, c2, t)
			// Subtle top highlight.
			hl := clamp01(1-(fy-float64(plate.Min.Y))/(s*0.45)) * 0.10
			c = lerp(c, color.NRGBA{0xFF, 0xFF, 0xFF, 0xFF}, hl)
			// Dark hairline at the very bottom for depth.
			if fy > s*0.93 {
				c = lerp(c, edge, clamp01((fy-s*0.93)/(s*0.07))*0.35)
			}
			img.Set(x, y, c)
		}
	}

	// Terminal prompt glyph: ">" plus an underscore.
	white := color.NRGBA{0xFF, 0xFF, 0xFF, 0xFF}
	stroke := s * 0.072
	// ">" chevron: 344,307 -> 469,512 -> 344,717 (scaled from the 1024 master)
	drawChevron(img, s*0.336, s*0.300, s*0.458, s*0.500, s*0.336, s*0.700, stroke, white)
	// Underscore: 486,719 -> 691,719
	drawLine(img, s*0.475, s*0.702, s*0.674, s*0.702, stroke, white)
	return img
}

// drawChevron strokes a ">" through the three given points.
func drawChevron(img *image.RGBA, x0, y0, x1, y1, x2, y2, stroke float64, c color.NRGBA) {
	drawLine(img, x0, y0, x1, y1, stroke, c)
	drawLine(img, x1, y1, x2, y2, stroke, c)
}

// drawLine strokes a thick line between two points.
func drawLine(img *image.RGBA, ax, ay, bx, by, stroke float64, c color.NRGBA) {
	dx, dy := bx-ax, by-ay
	length := math.Hypot(dx, dy)
	if length == 0 {
		return
	}
	steps := int(length*2) + 1
	half := stroke / 2
	for i := 0; i <= steps; i++ {
		t := float64(i) / float64(steps)
		px, py := ax+dx*t, ay+dy*t
		for oy := -half; oy <= half; oy += 0.5 {
			for ox := -half; ox <= half; ox += 0.5 {
				if ox*ox+oy*oy > half*half {
					continue
				}
				x := int(px + ox)
				y := int(py + oy)
				if x < 0 || y < 0 || x >= img.Bounds().Dx() || y >= img.Bounds().Dy() {
					continue
				}
				img.Set(x, y, c)
			}
		}
	}
}

// drawBar strokes a horizontal line.

func insideRounded(x, y float64, r image.Rectangle, radius float64) bool {
	minX, maxX := float64(r.Min.X), float64(r.Max.X)
	minY, maxY := float64(r.Min.Y), float64(r.Max.Y)
	cx := math.Min(math.Max(x, minX+radius), maxX-radius)
	cy := math.Min(math.Max(y, minY+radius), maxY-radius)
	dx, dy := x-cx, y-cy
	return dx*dx+dy*dy <= radius*radius
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func lerp(a, b color.NRGBA, t float64) color.NRGBA {
	t = clamp01(t)
	return color.NRGBA{
		R: uint8(float64(a.R) + (float64(b.R)-float64(a.R))*t),
		G: uint8(float64(a.G) + (float64(b.G)-float64(a.G))*t),
		B: uint8(float64(a.B) + (float64(b.B)-float64(a.B))*t),
		A: uint8(float64(a.A) + (float64(b.A)-float64(a.A))*t),
	}
}

func renderSVG() string {
	return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1024 1024" width="1024" height="1024" role="img" aria-label="mymac">
  <defs>
    <linearGradient id="g" x1="0" y1="0" x2="1" y2="1">
      <stop offset="0" stop-color="#0A84FF"/>
      <stop offset="1" stop-color="#6452E0"/>
    </linearGradient>
  </defs>
  <rect x="56" y="56" width="912" height="912" rx="229" fill="url(#g)"/>
  <path d="M344 307 L469 512 L344 717" fill="none" stroke="#ffffff" stroke-width="74" stroke-linecap="round" stroke-linejoin="round"/>
  <rect x="486" y="682" width="205" height="74" rx="37" fill="#ffffff"/>
</svg>
`
}
