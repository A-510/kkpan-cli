package cliapp

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/kkpan-cli/kkpan/internal/kkconfig"
	"github.com/kkpan-cli/kkpan/internal/ui"
	"github.com/kkpan-cli/kkpan/quarkpan"
)

func TestPreScanGlobalFlags(t *testing.T) {
	snap := ui.SnapshotOverrides()
	defer ui.RestoreOverrides(snap)

	ui.SetQuiet(false)
	ui.SetNoColor(false)
	ui.SetASCIIOnly(false)
	ui.SetThemeOverride("")

	preScanGlobalFlags([]string{"kkpan", "-q", "--no-color", "--ascii", "--theme", "shinbo", "ls", "/"})
	if !ui.IsQuiet() || !ui.ASCIIOnly() || ui.GetThemeOverride() != "shinbo" {
		t.Fatalf("preScanGlobalFlags 未正确设置全局开关: quiet=%v ascii=%v theme=%s",
			ui.IsQuiet(), ui.ASCIIOnly(), ui.GetThemeOverride())
	}
}

func TestEndToEndWhoEnvConfigAndSumfile(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv(kkconfig.EnvConfigDir, tmpDir)
	kkconfig.Manager = kkconfig.NewManager(filepath.Join(tmpDir, kkconfig.ConfigFileName))

	// 捕获 stdout 验证 who --json 未登录时的确定性结构化输出
	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w

	runErr := Run("v0.1.0-test", []string{"kkpan", "-q", "--no-color", "who", "--json"})
	_ = w.Close()
	os.Stdout = origStdout
	if runErr != nil {
		t.Fatalf("who --json 不应在未登录时失败: %v", runErr)
	}

	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	if strings.Contains(buf.String(), "\x1b") {
		t.Fatalf("启用 --no-color 后不应包含 ANSI 转义符: %q", buf.String())
	}

	var who map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &who); err != nil {
		t.Fatalf("who --json 输出非合法 JSON: %v\nraw=%s", err, buf.String())
	}
	if loggedIn, ok := who["logged_in"].(bool); !ok || loggedIn {
		t.Fatalf("未登录状态期望 logged_in=false，实际: %+v", who)
	}

	// 验证 env 与 config set -theme
	if err := Run("v0.1.0-test", []string{"kkpan", "-q", "--no-color", "config", "set", "-theme", "dossier"}); err != nil {
		t.Fatalf("config set -theme 失败: %v", err)
	}
	if kkconfig.Manager.Theme != "dossier" {
		t.Fatalf("主题设置未生效: %s", kkconfig.Manager.Theme)
	}

	// 验证 splitShellArgs
	tokens := splitShellArgs(`upload "C:\my files\demo.mp4" '/我的 番剧'`)
	if len(tokens) != 3 || tokens[1] != `C:\my files\demo.mp4` || tokens[2] != "/我的 番剧" {
		t.Fatalf("splitShellArgs 解析异常: %+v", tokens)
	}
}

func TestCloudCommandsRegistrationAndNotLoggedIn(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv(kkconfig.EnvConfigDir, tmpDir)
	kkconfig.Manager = kkconfig.NewManager(filepath.Join(tmpDir, kkconfig.ConfigFileName))

	app := NewApp("v0.1.0-test")
	for _, name := range []string{"transfer", "share", "recycle"} {
		if cmd := app.Command(name); cmd == nil {
			t.Fatalf("未注册阶段 3 命令: %s", name)
		}
	}
	shareCmd := app.Command("share")
	for _, sub := range []string{"set", "list", "cancel"} {
		if shareCmd.Command(sub) == nil {
			t.Fatalf("share 缺少子命令: %s", sub)
		}
	}
	recycleCmd := app.Command("recycle")
	for _, sub := range []string{"list", "restore", "delete"} {
		if recycleCmd.Command(sub) == nil {
			t.Fatalf("recycle 缺少子命令: %s", sub)
		}
	}

	// 验证未登录时调用阶段 3 命令返回 ErrNotLoggedIn 且不崩溃
	for _, argv := range [][]string{
		{"kkpan", "-q", "--no-color", "tf", "https://pan.quark.cn/s/abc123def456"},
		{"kkpan", "-q", "--no-color", "share", "list", "--json"},
		{"kkpan", "-q", "--no-color", "recycle", "list", "--json"},
	} {
		err := Run("v0.1.0-test", argv)
		if !errors.Is(err, quarkpan.ErrNotLoggedIn) {
			t.Fatalf("命令 %v 未登录时期望返回 ErrNotLoggedIn，实际: %v", argv, err)
		}
	}
}

func TestBTCommandRegistrationAndCLI(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv(kkconfig.EnvConfigDir, tmpDir)
	kkconfig.Manager = kkconfig.NewManager(filepath.Join(tmpDir, kkconfig.ConfigFileName))

	app := NewApp("v0.1.0-test")
	btCmd := app.Command("bt")
	if btCmd == nil {
		t.Fatalf("未注册 bt 命令")
	}
	for _, alias := range []string{"torrent", "magnet", "offlinedl"} {
		if !btCmd.HasName(alias) {
			t.Fatalf("bt 命令缺少别名: %s", alias)
		}
	}

	// 构造一个最小合法 .torrent 文件用于端到端 CLI 验证
	sampleFile := filepath.Join(tmpDir, "demo.bin")
	if err := os.WriteFile(sampleFile, []byte("hello-kkpan-torrent"), 0644); err != nil {
		t.Fatal(err)
	}
	info := metainfo.Info{PieceLength: 16384}
	if err := info.BuildFromFilePath(sampleFile); err != nil {
		t.Fatal(err)
	}
	infoBytes, err := bencode.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	torrentPath := filepath.Join(tmpDir, "demo.torrent")
	tf, err := os.Create(torrentPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := (&metainfo.MetaInfo{InfoBytes: infoBytes}).Write(tf); err != nil {
		_ = tf.Close()
		t.Fatal(err)
	}
	_ = tf.Close()

	// 1. 验证 bt --list --json 输出合法 JSON 且无 ANSI 转义符
	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	runErr := Run("v0.1.0-test", []string{"kkpan", "-q", "--no-color", "bt", "--list", "--json", torrentPath})
	_ = w.Close()
	os.Stdout = origStdout
	if runErr != nil {
		t.Fatalf("bt --list --json 执行失败: %v", runErr)
	}

	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	if strings.Contains(buf.String(), "\x1b") {
		t.Fatalf("启用 --no-color 后不应包含 ANSI 转义符: %q", buf.String())
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("bt --list --json 输出非合法 JSON: %v\nraw=%s", err, buf.String())
	}
	if parsed["name"] != "demo.bin" {
		t.Fatalf("期望种子名 demo.bin，实际: %+v", parsed)
	}

	// 2. 验证未登录时调用 bt --upload 立即返回 ErrNotLoggedIn
	if err := Run("v0.1.0-test", []string{"kkpan", "-q", "--no-color", "bt", "--upload", "/离线资源", torrentPath}); !errors.Is(err, quarkpan.ErrNotLoggedIn) {
		t.Fatalf("未登录调用 bt --upload 期望返回 ErrNotLoggedIn，实际: %v", err)
	}

	// 3. 验证 --delete-local 缺少 --upload 时立即参数报错
	if err := Run("v0.1.0-test", []string{"kkpan", "-q", "--no-color", "bt", "--delete-local", torrentPath}); err == nil {
		t.Fatalf("期望缺少 --upload 时 --delete-local 返回参数错误")
	}
}
