package ui

import (
	"fmt"
	"io"
	"os"
	"strings"

	qrcode "github.com/skip2/go-qrcode"
)

// WriteQRPNGFile 将登录 URL 编码为 PNG 二维码并以 0600 权限保存至指定路径
func WriteQRPNGFile(qrURL, filePath string) error {
	pngBytes, err := qrcode.Encode(qrURL, qrcode.Medium, 256)
	if err != nil {
		return fmt.Errorf("生成二维码 PNG 失败: %w", err)
	}
	if err := os.WriteFile(filePath, pngBytes, 0600); err != nil {
		return fmt.Errorf("保存临时二维码文件失败: %w", err)
	}
	return nil
}

// RenderQRMatrixTerminal 将指定 URL 编码为紧凑 1x2 Unicode 半块字符矩阵（反色高对比），
// 并在终端列宽不足以容纳矩阵宽度时返回空字符串提示调用方降级使用本地 PNG 图片。
func RenderQRMatrixTerminal(w io.Writer, qrURL string, colorOn, asciiOn bool) (string, int, error) {
	if asciiOn {
		return "", 0, nil
	}
	qr, err := qrcode.New(qrURL, qrcode.Medium)
	if err != nil {
		return "", 0, fmt.Errorf("构建二维码矩阵失败: %w", err)
	}
	bm := qr.Bitmap()
	if len(bm) == 0 {
		return "", 0, fmt.Errorf("二维码矩阵为空")
	}

	// 四周保留 1 格静默白边确保移动端摄像头稳定识别
	n := len(bm)
	paddedW := n + 2
	if w != nil && IsTTY(w) {
		cols := TerminalWidth()
		if cols > 0 && cols < paddedW {
			return "", paddedW, nil
		}
	}

	grid := make([][]bool, paddedW)
	for y := 0; y < paddedW; y++ {
		row := make([]bool, paddedW)
		for x := 0; x < paddedW; x++ {
			if y > 0 && y <= n && x > 0 && x <= n {
				row[x] = bm[y-1][x-1]
			}
		}
		grid[y] = row
	}

	var sb strings.Builder
	whiteFG := "\x1b[97m"
	blackBG := "\x1b[40m"
	reset := "\x1b[0m"

	for y := 0; y < paddedW; y += 2 {
		if colorOn {
			sb.WriteString(blackBG)
			sb.WriteString(whiteFG)
		}
		for x := 0; x < paddedW; x++ {
			topDark := grid[y][x]
			botDark := false
			if y+1 < paddedW {
				botDark = grid[y+1][x]
			}
			// 终端默认多为深色背景，对模块取反（空白处亮白、码点深黑）保证扫码对比度
			topLight := !topDark
			botLight := !botDark
			switch {
			case topLight && botLight:
				sb.WriteRune('█')
			case topLight && !botLight:
				sb.WriteRune('▀')
			case !topLight && botLight:
				sb.WriteRune('▄')
			default:
				sb.WriteRune(' ')
			}
		}
		if colorOn {
			sb.WriteString(reset)
		}
		if y+2 < paddedW {
			sb.WriteByte('\n')
		}
	}
	return sb.String(), paddedW, nil
}
