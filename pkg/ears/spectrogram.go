package ears

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

// Spectrogram layout. Muse reads it like an engineer would, so it carries
// labelled axes and a dB legend: without them the colours mean nothing.
const (
	plotW, plotH           = 1000, 360
	padL, padR, padT, padB = 70, 90, 28, 34
	minHz, maxHz           = 20.0, 12000.0
	floorDB                = -120.0
	stftSize, stftHop      = 2048, 512
)

// Spectrogram draws a: time across, log frequency up, level as colour in
// dBFS, titled with title.
func Spectrogram(a Audio, title string) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, padL+plotW+padR, padT+plotH+padB))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.Black), image.Point{}, draw.Src)

	frames := stft(a.Samples)
	binHz := float64(a.Rate) / stftSize
	for x := 0; x < plotW; x++ {
		fr := frames[min(len(frames)-1, x*len(frames)/plotW)]
		for y := 0; y < plotH; y++ {
			// row y (0 = top) → frequency on a log axis; take the loudest bin in the row's span
			lo := logHz(float64(plotH-y-1) / plotH)
			hi := logHz(float64(plotH-y) / plotH)
			b0, b1 := int(lo/binHz), int(math.Ceil(hi/binHz))
			db := floorDB
			for b := b0; b <= b1 && b < len(fr); b++ {
				db = math.Max(db, fr[b])
			}
			img.Set(padL+x, padT+y, heat(db))
		}
	}

	white := color.RGBA{230, 230, 230, 255}
	text(img, padL, 18, title, white)
	for _, hz := range []float64{50, 100, 200, 400, 1000, 2000, 3000, 6000, 10000} {
		y := padT + plotH - int(math.Log(hz/minHz)/math.Log(maxHz/minHz)*plotH)
		hline(img, padL-6, padL, y, white)
		text(img, 6, y+4, hzLabel(hz), white)
	}
	secs := float64(len(a.Samples)) / float64(a.Rate)
	for s := 0; float64(s) <= secs; s++ {
		x := padL + int(float64(s)/secs*plotW)
		vline(img, x, padT+plotH, padT+plotH+6, white)
		text(img, x-4, padT+plotH+20, fmt.Sprintf("%ds", s), white)
	}
	// legend: colour bar from 0 dBFS (top) to the floor
	lx := padL + plotW + 16
	for y := 0; y < plotH; y++ {
		c := heat(floorDB * float64(y) / plotH)
		for x := lx; x < lx+16; x++ {
			img.Set(x, padT+y, c)
		}
	}
	for db := 0.0; db >= floorDB; db -= 20 {
		y := padT + int(db/floorDB*plotH)
		text(img, lx+20, y+4, fmt.Sprintf("%.0f", db), white)
	}
	text(img, lx-2, padT-8, "dBFS", white)
	return img
}

// Stack places images top to bottom, for side-by-side comparison.
func Stack(imgs ...*image.RGBA) *image.RGBA {
	w, h := 0, 0
	for _, im := range imgs {
		w = max(w, im.Bounds().Dx())
		h += im.Bounds().Dy()
	}
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	y := 0
	for _, im := range imgs {
		draw.Draw(out, im.Bounds().Add(image.Pt(0, y)), im, image.Point{}, draw.Src)
		y += im.Bounds().Dy()
	}
	return out
}

// WritePNG saves img to path.
func WritePNG(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// stft returns per-frame levels in dBFS: a full-scale sine reads 0 dB.
func stft(x []float64) [][]float64 {
	win := hann(stftSize)
	var wsum float64
	for _, w := range win {
		wsum += w
	}
	ref := (wsum / 2) * (wsum / 2)
	buf := make([]complex128, stftSize)
	var out [][]float64
	for start := 0; start+stftSize <= len(x); start += stftHop {
		p := power(x[start:start+stftSize], win, buf)
		for i := range p {
			p[i] = math.Max(floorDB, 10*math.Log10(p[i]/ref+1e-30))
		}
		out = append(out, p)
	}
	if out == nil {
		out = [][]float64{make([]float64, stftSize/2+1)}
		for i := range out[0] {
			out[0][i] = floorDB
		}
	}
	return out
}

func logHz(t float64) float64 { return minHz * math.Pow(maxHz/minHz, t) }

// heat maps dBFS to black → purple → red → orange → yellow → white.
func heat(db float64) color.RGBA {
	t := math.Min(1, math.Max(0, 1-db/floorDB))
	stops := []struct {
		t       float64
		r, g, b float64
	}{{0, 0, 0, 0}, {0.3, 40, 0, 90}, {0.5, 150, 0, 110}, {0.65, 220, 30, 30}, {0.8, 250, 140, 0}, {0.92, 255, 230, 60}, {1, 255, 255, 255}}
	for i := 1; i < len(stops); i++ {
		if t <= stops[i].t {
			a, b := stops[i-1], stops[i]
			f := (t - a.t) / (b.t - a.t)
			return color.RGBA{uint8(a.r + f*(b.r-a.r)), uint8(a.g + f*(b.g-a.g)), uint8(a.b + f*(b.b-a.b)), 255}
		}
	}
	return color.RGBA{255, 255, 255, 255}
}

func hzLabel(hz float64) string {
	if hz >= 1000 {
		return fmt.Sprintf("%gk Hz", hz/1000)
	}
	return fmt.Sprintf("%g Hz", hz)
}

func text(img *image.RGBA, x, y int, s string, c color.Color) {
	d := font.Drawer{Dst: img, Src: image.NewUniform(c), Face: basicfont.Face7x13, Dot: fixed.P(x, y)}
	d.DrawString(s)
}

func hline(img *image.RGBA, x0, x1, y int, c color.Color) {
	for x := x0; x < x1; x++ {
		img.Set(x, y, c)
	}
}

func vline(img *image.RGBA, x, y0, y1 int, c color.Color) {
	for y := y0; y < y1; y++ {
		img.Set(x, y, c)
	}
}

// Painter implements core.Visualizer, writing numbered PNGs into Dir.
type Painter struct {
	Dir string
	n   int
}

func (p *Painter) Spectrogram(wavs, labels []string) (string, error) {
	var imgs []*image.RGBA
	for i, w := range wavs {
		a, err := ReadWAV(w)
		if err != nil {
			return "", err
		}
		label := w
		if i < len(labels) {
			label = labels[i]
		}
		imgs = append(imgs, Spectrogram(a, label))
	}
	if err := os.MkdirAll(p.Dir, 0o755); err != nil {
		return "", err
	}
	p.n++
	out := filepath.Join(p.Dir, fmt.Sprintf("spectrogram-%d-%03d.png", time.Now().Unix(), p.n))
	return out, WritePNG(out, Stack(imgs...))
}
