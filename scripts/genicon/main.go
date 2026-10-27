// Command genicon draws Marginalia's app icon, resources/icon.png: the
// app's logo (frontend/src/ui/Icon.jsx, Logo) at 1024×1024 on the macOS
// icon grid — a vermilion rounded square, #c8442b to #e2673f from the top-left
// to the bottom-right corner, its corners rounded by 22.5 %, with the white
// "M" of the logo.
//
// It is a tool of the repository, not part of the app:
//
//	go run ./scripts/genicon [-o resources/icon.png] [-size 1024]
//
// Shapes are rendered from their signed distance fields with one pixel of
// anti-aliasing, so the result is the same on every machine.
package main

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

// The logo is drawn in its own 32×32 coordinates (the SVG viewBox), mapped
// onto the body of the icon.
const (
	logoUnits = 32.0
	// cornerRadius is the rx of the logo's rect.
	cornerRadius = 7.2
	// strokeWidth is the width of the "M".
	strokeWidth = 2.6
	// bodyScale is the share of the canvas the rounded square takes: Apple's
	// grid puts an 824 px body on a 1024 px canvas, leaving room for the
	// system's shadow.
	bodyScale = 824.0 / 1024.0
)

// glyph is the "M" of the logo: M9 22.5 V10 l7 7.2 L23 10 v12.5.
var glyph = []point{{9, 22.5}, {9, 10}, {16, 17.2}, {23, 10}, {23, 22.5}}

// Gradient stops of the logo.
var (
	gradientFrom = rgb{0xc8, 0x44, 0x2b}
	gradientTo   = rgb{0xe2, 0x67, 0x3f}
	white        = rgb{0xff, 0xff, 0xff}
)

type point struct{ x, y float64 }

type rgb struct{ r, g, b float64 }

func main() {
	out := flag.String("o", filepath.Join("resources", "icon.png"), "output `file`")
	size := flag.Int("size", 1024, "width and height in `pixels`")
	flag.Parse()
	if *size < 16 {
		fmt.Fprintln(os.Stderr, "genicon: -size must be at least 16")
		os.Exit(2)
	}
	if err := write(*out, render(*size)); err != nil {
		fmt.Fprintln(os.Stderr, "genicon:", err)
		os.Exit(1)
	}
	fmt.Println("wrote", *out)
}

// render draws the icon at size×size pixels.
func render(size int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	s := float64(size)
	body := s * bodyScale
	origin := (s - body) / 2
	unit := body / logoUnits // pixels per logo unit
	radius := cornerRadius * unit
	half := body / 2
	center := point{s / 2, s / 2}
	m := make([]point, len(glyph))
	for i, p := range glyph {
		m[i] = point{origin + p.x*unit, origin + p.y*unit}
	}
	stroke := strokeWidth * unit / 2

	for y := range size {
		for x := range size {
			p := point{float64(x) + 0.5, float64(y) + 0.5}
			bodyCover := coverage(roundedRect(p, center, half, half, radius))
			if bodyCover == 0 {
				continue
			}
			// The logo's linearGradient runs from (0,0) to (1,1) in
			// bounding-box units: project onto the diagonal.
			t := clamp(((p.x-origin)+(p.y-origin))/(2*body), 0, 1)
			c := mix(gradientFrom, gradientTo, t)
			if glyphCover := coverage(polyline(p, m) - stroke); glyphCover > 0 {
				c = mix(c, white, glyphCover)
			}
			img.SetNRGBA(x, y, color.NRGBA{
				R: uint8(math.Round(c.r)),
				G: uint8(math.Round(c.g)),
				B: uint8(math.Round(c.b)),
				A: uint8(math.Round(255 * bodyCover)),
			})
		}
	}
	return img
}

// coverage turns a signed distance (negative inside) into the share of a
// pixel a shape covers, with one pixel of anti-aliasing.
func coverage(d float64) float64 { return clamp(0.5-d, 0, 1) }

// roundedRect is the signed distance from p to a rectangle centred on c
// with half sizes hw, hh and corner radius r.
func roundedRect(p, c point, hw, hh, r float64) float64 {
	qx := math.Abs(p.x-c.x) - hw + r
	qy := math.Abs(p.y-c.y) - hh + r
	outside := math.Hypot(math.Max(qx, 0), math.Max(qy, 0))
	inside := math.Min(math.Max(qx, qy), 0)
	return outside + inside - r
}

// polyline is the distance from p to the polyline through pts.
func polyline(p point, pts []point) float64 {
	d := math.Inf(1)
	for i := 1; i < len(pts); i++ {
		d = math.Min(d, segment(p, pts[i-1], pts[i]))
	}
	return d
}

// segment is the distance from p to the segment ab; stroking it gives
// round caps and joins, like stroke-linecap/linejoin="round".
func segment(p, a, b point) float64 {
	abx, aby := b.x-a.x, b.y-a.y
	apx, apy := p.x-a.x, p.y-a.y
	t := clamp((apx*abx+apy*aby)/(abx*abx+aby*aby), 0, 1)
	return math.Hypot(apx-t*abx, apy-t*aby)
}

func mix(a, b rgb, t float64) rgb {
	return rgb{a.r + (b.r-a.r)*t, a.g + (b.g-a.g)*t, a.b + (b.b-a.b)*t}
}

func clamp(v, lo, hi float64) float64 { return math.Min(math.Max(v, lo), hi) }

// write encodes img as PNG at path, creating its folder.
func write(path string, img image.Image) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(f, img); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
