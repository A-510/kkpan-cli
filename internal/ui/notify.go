package ui

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/kkpan-cli/kkpan/quarkpan"
)

// PrintSuccess 输出带成功徽标的提示信息
func PrintSuccess(w io.Writer, format string, a ...interface{}) {
	if w == nil {
		w = os.Stdout
	}
	p := ActivePalette()
	r := NewRenderer(w, nil)
	icon := "✦"
	if ASCIIOnly() {
		icon = "[OK]"
	}
	badge := r.NewStyle().Foreground(p.Success).Bold(true).Render(icon)
	fmt.Fprintf(w, "%s %s\n", badge, fmt.Sprintf(format, a...))
}

// PrintWarn 输出警告信息（默认写入 stderr 以免污染 stdout 数据流）
func PrintWarn(w io.Writer, format string, a ...interface{}) {
	if w == nil {
		w = os.Stderr
	}
	p := ActivePalette()
	r := NewRenderer(w, nil)
	icon := "!"
	if ASCIIOnly() {
		icon = "[!]"
	}
	badge := r.NewStyle().Foreground(p.Warn).Bold(true).Render(icon)
	fmt.Fprintf(w, "%s %s\n", badge, fmt.Sprintf(format, a...))
}

// PrintInfo 输出中性引导信息
func PrintInfo(w io.Writer, format string, a ...interface{}) {
	if w == nil {
		w = os.Stdout
	}
	p := ActivePalette()
	r := NewRenderer(w, nil)
	icon := "◈"
	if ASCIIOnly() {
		icon = "*"
	}
	badge := r.NewStyle().Foreground(p.Accent).Bold(true).Render(icon)
	fmt.Fprintf(w, "%s %s\n", badge, fmt.Sprintf(format, a...))
}

// Statusf 在非静默模式下向 stdout 打印状态文本
func Statusf(format string, a ...interface{}) {
	if IsQuiet() {
		return
	}
	fmt.Printf(format, a...)
}

// Statusln 在非静默模式下向 stdout 打印状态行
func Statusln(a ...interface{}) {
	if IsQuiet() {
		return
	}
	fmt.Println(a...)
}

// PrintError 以「错误 / 原因 / 建议」三段式结构输出错误详情
func PrintError(w io.Writer, title string, err error, hint string) {
	if w == nil {
		w = os.Stderr
	}
	fmt.Fprint(w, FormatError(w, title, err, hint, ColorEnabled(w), ASCIIOnly()))
}

// FormatError 生成三段式错误消息文本
func FormatError(w io.Writer, title string, err error, hint string, color, ascii bool) string {
	p := ActivePalette()
	r := NewRenderer(w, &color)
	icon := "✖"
	if ascii {
		icon = "x"
	}

	errStyle := r.NewStyle().Foreground(p.Fail).Bold(true)
	labelStyle := r.NewStyle().Foreground(p.Muted).Bold(true)
	hintStyle := r.NewStyle().Foreground(p.Secondary)

	var sb strings.Builder
	sb.WriteString(errStyle.Render(icon+" 错误: ") + title + "\n")
	if err != nil {
		sb.WriteString("  " + labelStyle.Render("原因:") + " " + err.Error() + "\n")
		if hint == "" {
			hint = FriendlyErrorHint(err)
		}
	}
	if hint != "" {
		sb.WriteString("  " + labelStyle.Render("建议:") + " " + hintStyle.Render(hint) + "\n")
	}
	return sb.String()
}

// FriendlyErrorHint 根据夸克网盘错误类型推断可执行的排错建议
func FriendlyErrorHint(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, quarkpan.ErrNotLoggedIn), errors.Is(err, quarkpan.ErrCookieExpired):
		return "运行 'kkpan login -qr' 扫码登录，或使用 'kkpan login -cookies \"...\"' 重新导入凭证。"
	case errors.Is(err, quarkpan.ErrNotFound):
		return "运行 'ls' 检查当前目录与文件拼写，或使用 'fid:<id>' 直连语法定位。"
	case errors.Is(err, quarkpan.ErrAlreadyExists):
		return "目标已存在同名项，可更换名称或调整上传 '--policy overwrite|rename'。"
	case errors.Is(err, quarkpan.ErrQuotaExceeded):
		return "夸克网盘容量不足，请清理无用文件或检查空间配额（'kkpan quota'）。"
	case errors.Is(err, quarkpan.ErrRateLimited):
		return "触发服务端频率限制，请稍后重试并保持普通账号并发 '-p 1'。"
	case errors.Is(err, quarkpan.ErrQRExpired):
		return "登录二维码已过期，请重新执行 'kkpan login -qr'。"
	case errors.Is(err, quarkpan.ErrSharePasscodeInvalid):
		return "请携带正确提取码重试，例如: kkpan transfer <分享链接> <4位提取码>。"
	case errors.Is(err, quarkpan.ErrShareInvalid):
		return "请确认分享链接完整、未过期且未被分享者取消。"
	case strings.Contains(err.Error(), "未找到命令"):
		return "运行 'kkpan help' 查看可用命令，或运行 'kkpan ui' 打开全屏文件管理器。"
	case isDownloadEOFOrReset(err.Error()):
		return "下载节点连接被断开（常见于海外代理或 TUN 规则拦截夸克 CDN）：请将 *.quark.cn、*.drive.quark.cn、*.alikunlun.com 加入代理直连(Direct)规则或临时关闭全局海外代理后重试。"
	default:
		return ""
	}
}

func isDownloadEOFOrReset(msg string) bool {
	lower := strings.ToLower(msg)
	hasDisconnect := strings.Contains(msg, ": EOF") ||
		strings.HasSuffix(strings.TrimSpace(msg), "EOF") ||
		strings.Contains(lower, "connection reset") ||
		strings.Contains(lower, "handshake")
	if !hasDisconnect {
		return false
	}
	return strings.Contains(lower, "drive.quark.cn") ||
		strings.Contains(msg, "探测下载链接失败") ||
		strings.Contains(msg, "下载请求")
}
