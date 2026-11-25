package kkconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/kkpan-cli/kkpan/quarkpan"
)

const (
	// EnvConfigDir 自定义配置目录的环境变量
	EnvConfigDir = "KKPAN_CONFIG_DIR"
	// ConfigFileName 配置文件名
	ConfigFileName = "kkpan_config.json"
	// HistoryFileName REPL 历史命令文件名
	HistoryFileName = "kkpan_history.txt"
)

// Account 描述单个夸克网盘已登录账号的本地持久化会话
type Account struct {
	UID        string `json:"uid"`
	Nickname   string `json:"nickname"`
	Cookie     string `json:"cookie"`
	Workdir    string `json:"workdir"`
	MemberType string `json:"member_type"`
}

// Config 存储全局客户端配置与多账号列表
type Config struct {
	ActiveUID string    `json:"active_uid"`
	Accounts  []Account `json:"accounts"`

	SaveDir           string `json:"savedir"`
	MaxParallel       int    `json:"max_parallel"`
	MaxUploadParallel int    `json:"max_upload_parallel"`
	MaxDownloadLoad   int    `json:"max_download_load"`
	MaxUploadLoad     int    `json:"max_upload_load"`
	MaxDownloadRate   int64  `json:"max_download_rate"`
	MaxUploadRate     int64  `json:"max_upload_rate"`

	Proxy       string `json:"proxy"`
	UserAgent   string `json:"user_agent"`
	NoCheck     bool   `json:"no_check"`
	ModifyMTime bool   `json:"modify_mtime"`
	UPolicy     string `json:"u_policy"`
	PromptStyle string `json:"prompt_style"`
	Theme       string `json:"theme"`

	mu         sync.Mutex
	configPath string
	client     *quarkpan.Client
	clientKey  string
}

// Manager 是全局默认配置管理器
var Manager = NewManager("")

// NewManager 创建配置管理器；若传入 configPath 则固定使用该路径
func NewManager(configPath string) *Config {
	c := &Config{configPath: configPath}
	c.setDefaults()
	return c
}

// GetConfigDir 按优先级解析配置目录：KKPAN_CONFIG_DIR > 可执行文件同目录 > 系统标准配置目录
func GetConfigDir() string {
	if envDir := strings.TrimSpace(os.Getenv(EnvConfigDir)); envDir != "" {
		return envDir
	}
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		if _, err := os.Stat(filepath.Join(exeDir, ConfigFileName)); err == nil {
			return exeDir
		}
	}
	if runtime.GOOS == "windows" {
		if appData := strings.TrimSpace(os.Getenv("APPDATA")); appData != "" {
			return filepath.Join(appData, "kkpan")
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".config", "kkpan")
	}
	return "."
}

func defaultSaveDir() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, "Downloads")
	}
	return "./Downloads"
}

func (c *Config) setDefaults() {
	c.SaveDir = defaultSaveDir()
	c.MaxParallel = 1
	c.MaxUploadParallel = 2
	c.MaxDownloadLoad = 3
	c.MaxUploadLoad = 2
	c.UserAgent = quarkpan.DefaultUserAgent
	c.UPolicy = "skip"
	c.PromptStyle = "starship"
	c.Theme = "celestial"
}

func (c *Config) resolvePath() string {
	if c.configPath != "" {
		return c.configPath
	}
	return filepath.Join(GetConfigDir(), ConfigFileName)
}

// ConfigPath 返回当前生效的配置文件完整路径
func (c *Config) ConfigPath() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.resolvePath()
}

// fix 校验并修复异常字段边界值，避免配置损坏导致运行崩溃
func (c *Config) fix() {
	if strings.TrimSpace(c.SaveDir) == "" {
		c.SaveDir = defaultSaveDir()
	}
	if c.MaxParallel <= 0 {
		c.MaxParallel = 1
	}
	if c.MaxUploadParallel <= 0 {
		c.MaxUploadParallel = 2
	}
	if c.MaxDownloadLoad <= 0 {
		c.MaxDownloadLoad = 3
	}
	if c.MaxUploadLoad <= 0 {
		c.MaxUploadLoad = 2
	}
	if c.MaxDownloadRate < 0 {
		c.MaxDownloadRate = 0
	}
	if c.MaxUploadRate < 0 {
		c.MaxUploadRate = 0
	}
	if strings.TrimSpace(c.UserAgent) == "" {
		c.UserAgent = quarkpan.DefaultUserAgent
	}
	switch strings.ToLower(strings.TrimSpace(c.UPolicy)) {
	case "skip", "overwrite", "rename":
		c.UPolicy = strings.ToLower(strings.TrimSpace(c.UPolicy))
	default:
		c.UPolicy = "skip"
	}
	switch strings.ToLower(strings.TrimSpace(c.PromptStyle)) {
	case "starship", "boxed":
		c.PromptStyle = strings.ToLower(strings.TrimSpace(c.PromptStyle))
	default:
		c.PromptStyle = "starship"
	}
	switch strings.ToLower(strings.TrimSpace(c.Theme)) {
	case "celestial", "dossier", "shinbo":
		c.Theme = strings.ToLower(strings.TrimSpace(c.Theme))
	default:
		c.Theme = "celestial"
	}
	for i := range c.Accounts {
		if strings.TrimSpace(c.Accounts[i].Workdir) == "" {
			c.Accounts[i].Workdir = "/"
		}
	}
}

// Init 从磁盘加载配置；若配置文件尚不存在则初始化默认值
func (c *Config) Init() error {
	return c.Reload()
}

// Reload 从磁盘重新读取配置文件
func (c *Config) Reload() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	p := c.resolvePath()
	raw, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			c.setDefaults()
			c.ActiveUID = ""
			c.Accounts = nil
			c.client = nil
			c.clientKey = ""
			return nil
		}
		return fmt.Errorf("读取配置文件失败: %w", err)
	}

	if len(strings.TrimSpace(string(raw))) == 0 {
		c.setDefaults()
		c.fix()
		return nil
	}

	// 先恢复默认字段，避免旧版本配置缺字段时变零值
	c.setDefaults()
	if err := json.Unmarshal(raw, c); err != nil {
		return fmt.Errorf("解析配置文件 JSON 失败: %w", err)
	}
	c.fix()
	return nil
}

// Save 将当前配置原子写入磁盘（目录 0700，文件 0600 保护 Cookie）
func (c *Config) Save() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.saveLocked()
}

func (c *Config) saveLocked() error {
	c.fix()
	p := c.resolvePath()
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("创建配置目录失败: %w", err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化配置失败: %w", err)
	}
	data = append(data, '\n')
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("写入配置临时文件失败: %w", err)
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("保存配置文件失败: %w", err)
	}
	return nil
}

// Reset 重置所有常规配置项（保留已登录账号列表）
func (c *Config) Reset() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	accounts := c.Accounts
	active := c.ActiveUID
	c.setDefaults()
	c.Accounts = accounts
	c.ActiveUID = active
	c.client = nil
	c.clientKey = ""
	return c.saveLocked()
}

// ActiveAccount 返回当前活跃账号副本；未登录时返回 nil
func (c *Config) ActiveAccount() *Account {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.Accounts {
		if c.Accounts[i].UID == c.ActiveUID {
			cp := c.Accounts[i]
			return &cp
		}
	}
	if len(c.Accounts) > 0 && c.ActiveUID == "" {
		cp := c.Accounts[0]
		return &cp
	}
	return nil
}

// SetAccount 新增或更新账号，并将其设为当前活跃账号
func (c *Config) SetAccount(acc Account) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if strings.TrimSpace(acc.Workdir) == "" {
		acc.Workdir = "/"
	}
	updated := false
	for i := range c.Accounts {
		if c.Accounts[i].UID == acc.UID {
			// 若保留了原有 Workdir 且新传入仅为默认值，保留旧目录记忆
			if acc.Workdir == "/" && c.Accounts[i].Workdir != "" {
				acc.Workdir = c.Accounts[i].Workdir
			}
			c.Accounts[i] = acc
			updated = true
			break
		}
	}
	if !updated {
		c.Accounts = append(c.Accounts, acc)
	}
	c.ActiveUID = acc.UID
	c.client = nil
	c.clientKey = ""
	return c.saveLocked()
}

// SwitchAccount 按 UID 或昵称切换当前活跃账号
func (c *Config) SwitchAccount(target string) (*Account, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	t := strings.TrimSpace(target)
	if t == "" {
		return nil, fmt.Errorf("请指定目标账号 UID 或昵称")
	}
	for i := range c.Accounts {
		if c.Accounts[i].UID == t || c.Accounts[i].Nickname == t {
			c.ActiveUID = c.Accounts[i].UID
			c.client = nil
			c.clientKey = ""
			if err := c.saveLocked(); err != nil {
				return nil, err
			}
			cp := c.Accounts[i]
			return &cp, nil
		}
	}
	return nil, fmt.Errorf("未找到账号 %q，请先运行 'loglist' 查看已登录账号", target)
}

// RemoveActiveAccount 注销当前活跃账号（或指定账号）
func (c *Config) RemoveAccount(uidOrName string) (*Account, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	target := strings.TrimSpace(uidOrName)
	if target == "" {
		target = c.ActiveUID
	}
	if target == "" {
		return nil, fmt.Errorf("当前没有已登录的账号")
	}

	idx := -1
	for i := range c.Accounts {
		if c.Accounts[i].UID == target || c.Accounts[i].Nickname == target {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil, fmt.Errorf("未找到账号 %q", target)
	}

	removed := c.Accounts[idx]
	c.Accounts = append(c.Accounts[:idx], c.Accounts[idx+1:]...)
	if c.ActiveUID == removed.UID {
		if len(c.Accounts) > 0 {
			c.ActiveUID = c.Accounts[0].UID
		} else {
			c.ActiveUID = ""
		}
	}
	c.client = nil
	c.clientKey = ""
	if err := c.saveLocked(); err != nil {
		return nil, err
	}
	return &removed, nil
}

// UpdateWorkdir 持久化更新当前活跃账号的工作目录
func (c *Config) UpdateWorkdir(workdir string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	wd := quarkpan.NormalizePath("/", workdir)
	for i := range c.Accounts {
		if c.Accounts[i].UID == c.ActiveUID {
			if c.Accounts[i].Workdir == wd {
				return nil
			}
			c.Accounts[i].Workdir = wd
			return c.saveLocked()
		}
	}
	return quarkpan.ErrNotLoggedIn
}

// ActiveClient 返回绑定当前活跃账号与 Cookie 自动刷新回调的 *quarkpan.Client
func (c *Config) ActiveClient() (*quarkpan.Client, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	var active *Account
	for i := range c.Accounts {
		if c.Accounts[i].UID == c.ActiveUID {
			active = &c.Accounts[i]
			break
		}
	}
	cookie := ""
	uid := ""
	if active != nil {
		cookie = active.Cookie
		uid = active.UID
	}

	key := uid + "|" + c.Proxy + "|" + c.UserAgent
	if c.client != nil && c.clientKey == key {
		if cookie != "" && c.client.CookieHeader() != cookie {
			c.client.SetCookie(cookie)
		}
		return c.client, nil
	}

	targetUID := uid
	cli, err := quarkpan.NewClient(quarkpan.ClientOptions{
		UserAgent: c.UserAgent,
		ProxyURL:  c.Proxy,
		Cookie:    cookie,
		OnCookieUpdated: func(newCookie string) {
			if targetUID == "" {
				return
			}
			c.updateAccountCookie(targetUID, newCookie)
		},
	})
	if err != nil {
		return nil, err
	}
	c.client = cli
	c.clientKey = key
	return cli, nil
}

func (c *Config) updateAccountCookie(uid, newCookie string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.Accounts {
		if c.Accounts[i].UID == uid {
			if c.Accounts[i].Cookie == newCookie {
				return
			}
			c.Accounts[i].Cookie = newCookie
			_ = c.saveLocked()
			return
		}
	}
}

// NewAnonClient 创建不绑定已保存账号的临时客户端（用于扫码或 Cookie 登录阶段）
func (c *Config) NewAnonClient() (*quarkpan.Client, error) {
	c.mu.Lock()
	proxy := c.Proxy
	ua := c.UserAgent
	c.mu.Unlock()
	return quarkpan.NewClient(quarkpan.ClientOptions{
		ProxyURL:  proxy,
		UserAgent: ua,
	})
}

// ParseByteRate 解析诸如 "20MB/s"、"512KB"、"0" 等速率字符串为每秒字节数
func ParseByteRate(raw string) (int64, error) {
	s := strings.TrimSpace(strings.ToUpper(raw))
	s = strings.TrimSuffix(s, "/S")
	s = strings.TrimSuffix(s, "PS")
	s = strings.TrimSpace(s)
	if s == "" || s == "0" || s == "UNLIMITED" {
		return 0, nil
	}

	units := []struct {
		suffix string
		mult   float64
	}{
		{"GIB", 1024 * 1024 * 1024},
		{"GB", 1024 * 1024 * 1024},
		{"G", 1024 * 1024 * 1024},
		{"MIB", 1024 * 1024},
		{"MB", 1024 * 1024},
		{"M", 1024 * 1024},
		{"KIB", 1024},
		{"KB", 1024},
		{"K", 1024},
		{"B", 1},
	}

	for _, u := range units {
		if strings.HasSuffix(s, u.suffix) {
			numStr := strings.TrimSpace(strings.TrimSuffix(s, u.suffix))
			val, err := strconv.ParseFloat(numStr, 64)
			if err != nil || val < 0 {
				return 0, fmt.Errorf("无效的速率格式: %q", raw)
			}
			return int64(val * u.mult), nil
		}
	}
	val, err := strconv.ParseFloat(s, 64)
	if err != nil || val < 0 {
		return 0, fmt.Errorf("无效的速率格式: %q", raw)
	}
	return int64(val), nil
}

// FormatByteRate 将每秒字节数格式化为可读字符串
func FormatByteRate(bytesPerSec int64) string {
	if bytesPerSec <= 0 {
		return "不限速"
	}
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
	)
	switch {
	case bytesPerSec >= gb:
		return fmt.Sprintf("%.2f GB/s", float64(bytesPerSec)/gb)
	case bytesPerSec >= mb:
		return fmt.Sprintf("%.2f MB/s", float64(bytesPerSec)/mb)
	case bytesPerSec >= kb:
		return fmt.Sprintf("%.1f KB/s", float64(bytesPerSec)/kb)
	default:
		return fmt.Sprintf("%d B/s", bytesPerSec)
	}
}
