package kkcommand

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/kkpan-cli/kkpan/internal/kkconfig"
	"github.com/kkpan-cli/kkpan/internal/transfer"
	"github.com/kkpan-cli/kkpan/internal/ui"
)

// RunConfigShow 以脱敏表格形式展示当前客户端运行配置（绝不输出任何 Cookie 原文）
func RunConfigShow() error {
	c := kkconfig.Manager
	proxyVal := c.Proxy
	if proxyVal == "" {
		proxyVal = "(未设置)"
	}
	rows := [][]string{
		{"config_path", c.ConfigPath(), "本地配置文件路径 (0600)"},
		{"savedir", c.SaveDir, "默认下载保存目录"},
		{"max_parallel", strconv.Itoa(c.MaxParallel), "单文件下载线程数"},
		{"max_upload_parallel", strconv.Itoa(c.MaxUploadParallel), "单文件上传并发分片数"},
		{"max_download_load", strconv.Itoa(c.MaxDownloadLoad), "同时下载文件数量"},
		{"max_upload_load", strconv.Itoa(c.MaxUploadLoad), "同时上传文件数量"},
		{"max_download_rate", kkconfig.FormatByteRate(c.MaxDownloadRate), "全局下载限速"},
		{"max_upload_rate", kkconfig.FormatByteRate(c.MaxUploadRate), "全局上传限速"},
		{"u_policy", c.UPolicy, "上传重名策略 (skip/overwrite/rename)"},
		{"no_check", strconv.FormatBool(c.NoCheck), "是否禁用下载 MD5 校验"},
		{"modify_mtime", strconv.FormatBool(c.ModifyMTime), "是否同步服务端修改时间"},
		{"proxy", proxyVal, "HTTP/SOCKS5 代理地址"},
		{"theme", c.Theme, "日式二次元主题 (celestial/dossier/shinbo)"},
		{"prompt_style", c.PromptStyle, "REPL 提示符风格 (starship/boxed)"},
	}
	fmt.Print(ui.RenderTable(os.Stdout, ui.TableConfig{
		Headers: []string{"配置项", "当前值", "说明"},
		Rows:    rows,
	}))
	return nil
}

// RunConfigSet 修改并持久化配置项
func RunConfigSet(updates map[string]string) error {
	if len(updates) == 0 {
		return fmt.Errorf("未提供任何需要修改的配置项")
	}
	c := kkconfig.Manager
	for k, v := range updates {
		val := strings.TrimSpace(v)
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "savedir":
			c.SaveDir = val
		case "max_parallel":
			n, err := strconv.Atoi(val)
			if err != nil || n <= 0 {
				return fmt.Errorf("max_parallel 必须为正整数")
			}
			c.MaxParallel = n
		case "max_upload_parallel":
			n, err := strconv.Atoi(val)
			if err != nil || n <= 0 {
				return fmt.Errorf("max_upload_parallel 必须为正整数")
			}
			c.MaxUploadParallel = n
		case "max_download_load":
			n, err := strconv.Atoi(val)
			if err != nil || n <= 0 {
				return fmt.Errorf("max_download_load 必须为正整数")
			}
			c.MaxDownloadLoad = n
		case "max_upload_load":
			n, err := strconv.Atoi(val)
			if err != nil || n <= 0 {
				return fmt.Errorf("max_upload_load 必须为正整数")
			}
			c.MaxUploadLoad = n
		case "max_download_rate":
			r, err := kkconfig.ParseByteRate(val)
			if err != nil {
				return err
			}
			c.MaxDownloadRate = r
		case "max_upload_rate":
			r, err := kkconfig.ParseByteRate(val)
			if err != nil {
				return err
			}
			c.MaxUploadRate = r
		case "u_policy":
			switch strings.ToLower(val) {
			case "skip", "overwrite", "rename":
				c.UPolicy = strings.ToLower(val)
			default:
				return fmt.Errorf("u_policy 仅支持 skip / overwrite / rename")
			}
		case "no_check":
			b, err := strconv.ParseBool(val)
			if err != nil {
				return err
			}
			c.NoCheck = b
		case "modify_mtime":
			b, err := strconv.ParseBool(val)
			if err != nil {
				return err
			}
			c.ModifyMTime = b
		case "proxy":
			c.Proxy = val
		case "theme":
			t, err := ui.ParseThemeName(val)
			if err != nil {
				return err
			}
			c.Theme = string(t)
		case "prompt_style":
			p, err := ui.ParsePromptStyle(val)
			if err != nil {
				return err
			}
			c.PromptStyle = string(p)
		default:
			return fmt.Errorf("未知配置项: %s", k)
		}
	}
	if err := c.Save(); err != nil {
		return err
	}
	ui.PrintSuccess(os.Stdout, "配置已保存")
	return nil
}

// RunConfigReset 重置常规配置为默认值
func RunConfigReset() error {
	if err := kkconfig.Manager.Reset(); err != nil {
		return err
	}
	ui.PrintSuccess(os.Stdout, "已重置为默认配置 (保留账号会话)")
	return nil
}

// RunEnv 打印环境变量状态及生效路径
func RunEnv() error {
	envVal := func(name string) string {
		if v := os.Getenv(name); v != "" {
			return v
		}
		return "(未设置)"
	}
	rows := [][]string{
		{kkconfig.EnvConfigDir, envVal(kkconfig.EnvConfigDir), "自定义配置目录 (当前: " + kkconfig.GetConfigDir() + ")"},
		{ui.EnvTheme, envVal(ui.EnvTheme), "日式二次元主题 (当前: " + string(ui.EffectiveTheme(kkconfig.Manager.Theme).Name) + ")"},
		{ui.EnvPromptStyle, envVal(ui.EnvPromptStyle), "REPL 提示符风格 (当前: " + string(ui.EffectivePromptStyle(kkconfig.Manager.PromptStyle)) + ")"},
		{ui.EnvASCII, envVal(ui.EnvASCII), "强制纯 7-bit ASCII 降级"},
		{"NO_COLOR", envVal("NO_COLOR"), "禁用 ANSI 彩色输出"},
	}
	fmt.Print(ui.RenderTable(os.Stdout, ui.TableConfig{
		Headers: []string{"环境变量", "当前值", "用途"},
		Rows:    rows,
	}))
	return nil
}

// RunSumFile 单遍流式计算一个或多个本地文件的大小、MD5 与 SHA1（对齐夸克秒传哈希校验）
func RunSumFile(paths []string) error {
	if len(paths) == 0 {
		return fmt.Errorf("请指定至少一个本地文件路径")
	}
	rows := make([][]string, 0, len(paths))
	for _, p := range paths {
		md5Hex, sha1Hex, sz, err := transfer.HashFileSinglePass(p)
		if err != nil {
			return fmt.Errorf("计算 %s 哈希失败: %w", p, err)
		}
		rows = append(rows, []string{p, fmt.Sprintf("%s (%d B)", ui.FormatBytes(sz), sz), md5Hex, sha1Hex})
	}
	fmt.Print(ui.RenderTable(os.Stdout, ui.TableConfig{
		Headers: []string{"文件", "体积", "MD5", "SHA1"},
		Rows:    rows,
	}))
	return nil
}
