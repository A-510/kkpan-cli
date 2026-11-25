package ui

import (
	"bytes"
	_ "embed"
	"fmt"
	"image/jpeg"
	"os"
	"strings"
	"time"

	"github.com/muesli/termenv"
	"golang.org/x/term"
)

const (
	mascotWidth       = 28
	mascotHeight      = 10
	mascotImageWidth  = 26
	mascotImageHeight = 18
)

// 原图由用户为 REPL 吉祥物提供。
//
//go:embed mascot.jpg
var mascotJPEG []byte

// PrintREPLBanner 在行编辑器接管光标前播放一次启动动画。
func PrintREPLBanner(out, in *os.File, info BannerInfo) {
	cols, rows, err := term.GetSize(int(out.Fd()))
	if err != nil || cols < 76 || rows < mascotHeight+5 || !IsTTY(in) || !ColorEnabled(out) || ASCIIOnly() {
		fmt.Fprint(out, RenderBanner(out, info))
		return
	}
	info.TermWidth = cols

	profile := NewRenderer(out, nil).ColorProfile()
	pixels, ok := sampleMascot(profile)
	if !ok {
		fmt.Fprint(out, RenderBanner(out, info))
		return
	}

	finalPanel := renderBanner(out, info, mascotFrame(pixels, 1, 2))
	panelRows := strings.Count(finalPanel, "\n")
	if os.Getenv("KKPAN_REDUCE_MOTION") == "1" || panelRows >= rows {
		fmt.Fprint(out, finalPanel)
		return
	}

	// 角色从左下探头，上扬后回到居中位置。纵向偏移以半格计。
	poses := [...]struct {
		x, y int
		hold time.Duration
	}{
		{0, 2, 120 * time.Millisecond},
		{1, 1, 90 * time.Millisecond},
		{2, 0, 140 * time.Millisecond},
		{1, 1, 90 * time.Millisecond},
		{1, 2, 0},
	}

	for i, pose := range poses {
		move := ""
		if i > 0 {
			w, h, err := term.GetSize(int(out.Fd()))
			if err != nil || w != cols || h != rows {
				return
			}
			move = fmt.Sprintf("\x1b[%dF", panelRows)
		}

		panel := renderBanner(out, info, mascotFrame(pixels, pose.x, pose.y))
		if _, err := fmt.Fprint(out, move+panel); err != nil {
			return
		}
		time.Sleep(pose.hold)
	}
}

// sampleMascot 将原图按终端半格采样，近白的背景保留为终端底色。
func sampleMascot(profile termenv.Profile) ([mascotImageHeight][mascotImageWidth]termenv.Color, bool) {
	var pixels [mascotImageHeight][mascotImageWidth]termenv.Color
	img, err := jpeg.Decode(bytes.NewReader(mascotJPEG))
	if err != nil {
		return pixels, false
	}

	bounds := img.Bounds()
	bounds.Min.X += bounds.Dx() / 12
	bounds.Max.X -= img.Bounds().Dx() / 16
	for y := range pixels {
		y0 := bounds.Min.Y + y*bounds.Dy()/mascotImageHeight
		y1 := bounds.Min.Y + (y+1)*bounds.Dy()/mascotImageHeight
		for x := range pixels[y] {
			x0 := bounds.Min.X + x*bounds.Dx()/mascotImageWidth
			x1 := bounds.Min.X + (x+1)*bounds.Dx()/mascotImageWidth
			var red, green, blue, count int
			for py := y0; py < y1; py++ {
				for px := x0; px < x1; px++ {
					r, g, b, _ := img.At(px, py).RGBA()
					if r >= 0xfcfc && g >= 0xfcfc && b >= 0xfcfc {
						continue
					}
					red += int(r >> 8)
					green += int(g >> 8)
					blue += int(b >> 8)
					count++
				}
			}
			if count*2 >= (x1-x0)*(y1-y0) {
				rgb := fmt.Sprintf("#%02x%02x%02x", red/count, green/count, blue/count)
				pixels[y][x] = profile.Color(rgb)
			}
		}
	}
	return pixels, true
}

func mascotFrame(pixels [mascotImageHeight][mascotImageWidth]termenv.Color, dx, dy int) string {
	var canvas [mascotHeight * 2][mascotWidth]termenv.Color
	for y := range pixels {
		copy(canvas[y+dy][dx:], pixels[y][:])
	}

	var frame strings.Builder
	for y := 0; y < len(canvas); y += 2 {
		if y > 0 {
			frame.WriteByte('\n')
		}
		for x, upper := range canvas[y] {
			lower := canvas[y+1][x]
			style := termenv.String()
			switch {
			case upper != nil && lower != nil:
				frame.WriteString(style.Foreground(upper).Background(lower).Styled("▀"))
			case upper != nil:
				frame.WriteString(style.Foreground(upper).Styled("▀"))
			case lower != nil:
				frame.WriteString(style.Foreground(lower).Styled("▄"))
			default:
				frame.WriteByte(' ')
			}
		}
	}
	return frame.String()
}
