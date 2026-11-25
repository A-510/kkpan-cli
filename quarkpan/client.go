package quarkpan

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultDriveBaseURL 夸克网盘 PC 客户端接口域名
	DefaultDriveBaseURL = "https://drive-pc.quark.cn"
	// DefaultPanBaseURL 夸克网盘 Web 账号域名
	DefaultPanBaseURL = "https://pan.quark.cn"
	// DefaultUOPBaseURL 阿里/夸克统一身份认证（扫码登录）域名
	DefaultUOPBaseURL = "https://uop.quark.cn"
	// DefaultUserAgent 模拟夸克网盘 PC 客户端 User-Agent
	DefaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) quark-cloud-drive/2.5.20 Chrome/100.0.4896.160 Electron/18.3.5.4-b478491100 Safari/537.36 Channel/pckk_other_ch"
	// DefaultReferer 夸克网盘请求默认 Referer
	DefaultReferer = "https://pan.quark.cn/"
	// DefaultOrigin 夸克网盘请求默认 Origin
	DefaultOrigin = "https://pan.quark.cn"
)

// ClientOptions 用于自定义夸克网盘客户端配置（支持测试注入与代理配置）
type ClientOptions struct {
	DriveBaseURL    string
	PanBaseURL      string
	UOPBaseURL      string
	UserAgent       string
	Cookie          string
	ProxyURL        string
	Timeout         time.Duration
	OnCookieUpdated func(cookieHeader string)
}

// Client 封装夸克网盘纯协议客户端，不包含任何终端 UI 依赖
type Client struct {
	driveBaseURL       string
	panBaseURL         string
	uopBaseURL         string
	userAgent          string
	httpClient         *http.Client
	downloadHTTPClient *http.Client
	onCookieUpdated    func(cookieHeader string)

	cookieMu sync.RWMutex
	cookies  map[string]string

	resolver *PathResolver
}

// NewClient 创建夸克网盘协议客户端实例
func NewClient(opts ClientOptions) (*Client, error) {
	driveBase := strings.TrimRight(strings.TrimSpace(opts.DriveBaseURL), "/")
	if driveBase == "" {
		driveBase = DefaultDriveBaseURL
	}
	panBase := strings.TrimRight(strings.TrimSpace(opts.PanBaseURL), "/")
	if panBase == "" {
		panBase = DefaultPanBaseURL
	}
	uopBase := strings.TrimRight(strings.TrimSpace(opts.UOPBaseURL), "/")
	if uopBase == "" {
		uopBase = DefaultUOPBaseURL
	}
	ua := strings.TrimSpace(opts.UserAgent)
	if ua == "" {
		ua = DefaultUserAgent
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	if proxyRaw := strings.TrimSpace(opts.ProxyURL); proxyRaw != "" {
		proxyParsed, err := url.Parse(proxyRaw)
		if err != nil {
			return nil, fmt.Errorf("解析代理地址失败: %w", err)
		}
		transport.Proxy = http.ProxyURL(proxyParsed)
	}

	jar, _ := cookiejar.New(nil)
	httpClient := &http.Client{
		Timeout:   timeout,
		Transport: transport,
		Jar:       jar,
	}

	// 专用下载客户端：取消整文件 30s 总超时限制（改用首包头超时），且不挂载 Jar 避免与手工 Cookie 头重复；
	// 重定向时保留 UA/Referer，仅同属 quark.cn 域时透传会话 Cookie；扩大同域连接池以支持多文件并行下载。
	dlTransport := transport.Clone()
	dlTransport.ResponseHeaderTimeout = 30 * time.Second
	dlTransport.MaxIdleConns = 64
	dlTransport.MaxIdleConnsPerHost = 32

	c := &Client{
		driveBaseURL:    driveBase,
		panBaseURL:      panBase,
		uopBaseURL:      uopBase,
		userAgent:       ua,
		httpClient:      httpClient,
		onCookieUpdated: opts.OnCookieUpdated,
		cookies:         parseCookieHeader(opts.Cookie),
	}
	c.downloadHTTPClient = &http.Client{
		Timeout:   0,
		Transport: dlTransport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("下载重定向次数过多")
			}
			req.Header.Set("User-Agent", c.userAgent)
			req.Header.Set("Referer", DefaultReferer)
			host := strings.ToLower(req.URL.Hostname())
			if host == "quark.cn" || strings.HasSuffix(host, ".quark.cn") {
				if ck := c.CookieHeader(); ck != "" {
					req.Header.Set("Cookie", ck)
				}
			} else {
				req.Header.Del("Cookie")
			}
			return nil
		},
	}
	c.resolver = NewPathResolver(c, 60*time.Second)
	return c, nil
}

// SetCookie 更新客户端内存中的 Cookie 映射（不触发持久化回调）
func (c *Client) SetCookie(rawCookie string) {
	parsed := parseCookieHeader(rawCookie)
	c.cookieMu.Lock()
	c.cookies = parsed
	c.cookieMu.Unlock()
	if c.resolver != nil {
		c.resolver.InvalidateAll()
	}
}

// CookieHeader 返回确定性排序后的 Cookie 请求头字符串
func (c *Client) CookieHeader() string {
	c.cookieMu.RLock()
	defer c.cookieMu.RUnlock()
	return formatCookieMap(c.cookies)
}

// HasCookie 判断客户端是否持有非空登录凭证
func (c *Client) HasCookie() bool {
	c.cookieMu.RLock()
	defer c.cookieMu.RUnlock()
	return len(c.cookies) > 0
}

// Resolver 返回内置的路径与 FID 双向解析器
func (c *Client) Resolver() *PathResolver {
	return c.resolver
}

// HTTPClient 返回底层的 HTTP 客户端（供接口调用与上传引擎复用）
func (c *Client) HTTPClient() *http.Client {
	return c.httpClient
}

// DownloadHTTPClient 返回专用于大文件流式与 Range 下载的 HTTP 客户端
func (c *Client) DownloadHTTPClient() *http.Client {
	if c.downloadHTTPClient != nil {
		return c.downloadHTTPClient
	}
	return c.httpClient
}

// UserAgent 返回当前客户端使用的 User-Agent
func (c *Client) UserAgent() string {
	return c.userAgent
}

// ApplyDownloadHeaders 为直链下载请求注入一致的会话 Cookie、UA 与 Referer
func (c *Client) ApplyDownloadHeaders(req *http.Request) {
	if req == nil {
		return
	}
	if cookie := c.CookieHeader(); cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Referer", DefaultReferer)
}

// parseCookieHeader 将 Cookie 请求头文本解析为键值表，保留最后出现的同名字段
func parseCookieHeader(raw string) map[string]string {
	out := make(map[string]string)
	for _, seg := range strings.Split(raw, ";") {
		part := strings.TrimSpace(seg)
		if part == "" {
			continue
		}
		eq := strings.IndexByte(part, '=')
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(part[:eq])
		val := strings.TrimSpace(part[eq+1:])
		if key != "" && val != "" {
			out[key] = val
		}
	}
	return out
}

func formatCookieMap(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+m[k])
	}
	return strings.Join(parts, "; ")
}

// mergeResponseCookies 拦截响应 Set-Cookie，若有变更则刷新内存态并通过回调持久化（用于自动续期 __puus）
func (c *Client) mergeResponseCookies(resp *http.Response) {
	if resp == nil {
		return
	}
	setCookies := resp.Cookies()
	if len(setCookies) == 0 {
		return
	}

	c.cookieMu.Lock()
	changed := false
	for _, ck := range setCookies {
		if ck == nil || ck.Name == "" {
			continue
		}
		if ck.MaxAge < 0 {
			if _, exists := c.cookies[ck.Name]; exists {
				delete(c.cookies, ck.Name)
				changed = true
			}
			continue
		}
		if ck.Value != "" && c.cookies[ck.Name] != ck.Value {
			c.cookies[ck.Name] = ck.Value
			changed = true
		}
	}
	var updatedHeader string
	if changed {
		updatedHeader = formatCookieMap(c.cookies)
	}
	cb := c.onCookieUpdated
	c.cookieMu.Unlock()

	if changed && cb != nil {
		cb(updatedHeader)
	}
}

type apiEnvelope struct {
	Status   int             `json:"status"`
	Code     int             `json:"code"`
	Message  string          `json:"message"`
	Data     json.RawMessage `json:"data"`
	Metadata json.RawMessage `json:"metadata"`
}

type apiMetadata struct {
	Total int `json:"_total"`
	Count int `json:"_count"`
	Page  int `json:"_page"`
	Size  int `json:"_size"`
}

// buildDriveURL 构造指向 drive-pc.quark.cn 的完整接口地址并自动补充公共查询参数
func (c *Client) buildDriveURL(apiPath string, query url.Values) string {
	q := url.Values{}
	for k, vals := range query {
		for _, v := range vals {
			q.Add(k, v)
		}
	}
	if !q.Has("pr") {
		q.Set("pr", "ucpro")
	}
	if !q.Has("fr") {
		q.Set("fr", "pc")
	}
	if !q.Has("uc_param_str") {
		q.Set("uc_param_str", "")
	}
	path := "/" + strings.TrimLeft(apiPath, "/")
	return c.driveBaseURL + path + "?" + q.Encode()
}

// doDriveJSON 执行 drive-pc 接口请求，自动注入会话头、刷新 Cookie 并解包标准响应信封
func (c *Client) doDriveJSON(ctx context.Context, op, method, apiPath string, query url.Values, reqBody interface{}, outData interface{}) error {
	return c.doDriveJSONWithMeta(ctx, op, method, apiPath, query, reqBody, outData, nil)
}

// doDriveJSONWithMeta 执行 drive-pc 接口请求，并在需要时同时解包信封中的分页 metadata
func (c *Client) doDriveJSONWithMeta(ctx context.Context, op, method, apiPath string, query url.Values, reqBody interface{}, outData interface{}, outMeta *apiMetadata) error {
	if !c.HasCookie() {
		return ErrNotLoggedIn
	}
	fullURL := c.buildDriveURL(apiPath, query)
	return c.doRawJSONWithMeta(ctx, op, method, fullURL, reqBody, true, outData, outMeta)
}

func (c *Client) doRawJSON(ctx context.Context, op, method, fullURL string, reqBody interface{}, unwrapEnvelope bool, outData interface{}) error {
	return c.doRawJSONWithMeta(ctx, op, method, fullURL, reqBody, unwrapEnvelope, outData, nil)
}

func (c *Client) doRawJSONWithMeta(ctx context.Context, op, method, fullURL string, reqBody interface{}, unwrapEnvelope bool, outData interface{}, outMeta *apiMetadata) error {
	var bodyReader io.Reader
	if reqBody != nil {
		raw, err := json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("%s 序列化请求体失败: %w", op, err)
		}
		bodyReader = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, fullURL, bodyReader)
	if err != nil {
		return fmt.Errorf("%s 构建请求失败: %w", op, err)
	}

	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Referer", DefaultReferer)
	req.Header.Set("Origin", DefaultOrigin)
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json;charset=UTF-8")
	}
	if cookie := c.CookieHeader(); cookie != "" {
		req.Header.Set("Cookie", cookie)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("%s 请求失败: %w", op, err)
	}
	defer resp.Body.Close()

	c.mergeResponseCookies(resp)

	rawResp, err := io.ReadAll(io.LimitReader(resp.Body, 16*1024*1024))
	if err != nil {
		return fmt.Errorf("%s 读取响应失败: %w", op, err)
	}

	if !unwrapEnvelope {
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return classifyError(op, resp.StatusCode, 0, 0, string(rawResp))
		}
		if outData != nil {
			if err := json.Unmarshal(rawResp, outData); err != nil {
				return fmt.Errorf("%s 解析响应 JSON 失败: %w", op, err)
			}
		}
		return nil
	}

	var env apiEnvelope
	if len(bytes.TrimSpace(rawResp)) > 0 {
		if err := json.Unmarshal(rawResp, &env); err != nil {
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				return classifyError(op, resp.StatusCode, 0, 0, http.StatusText(resp.StatusCode))
			}
			return fmt.Errorf("%s 解析响应信封失败: %w", op, err)
		}
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 || (env.Status != 0 && env.Status >= 400) || env.Code != 0 {
		msg := env.Message
		if msg == "" {
			msg = http.StatusText(resp.StatusCode)
		}
		return classifyError(op, resp.StatusCode, env.Status, env.Code, msg)
	}

	if outData != nil && len(env.Data) > 0 && string(env.Data) != "null" {
		if err := json.Unmarshal(env.Data, outData); err != nil {
			return fmt.Errorf("%s 解析响应数据失败: %w", op, err)
		}
	}
	if outMeta != nil && len(env.Metadata) > 0 && string(env.Metadata) != "null" {
		if err := json.Unmarshal(env.Metadata, outMeta); err != nil {
			return fmt.Errorf("%s 解析分页元数据失败: %w", op, err)
		}
	}
	return nil
}
