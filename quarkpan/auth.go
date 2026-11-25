package quarkpan

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

const (
	qrClientID = "532"
	qrVersion  = "1.2"
	// qrScanBaseURL 夸克移动端扫码登录确认页地址前缀
	qrScanBaseURL = "https://su.quark.cn/4_eMHBJ"
)

// generateUUIDv4 使用纯标准库加密随机数生成 UUID v4（用作扫码登录 request_id）
func generateUUIDv4() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("生成随机 request_id 失败: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// GetAccountInfo 获取当前已登录夸克账号的基本信息（不含敏感凭证）
func (c *Client) GetAccountInfo(ctx context.Context) (*AccountInfo, error) {
	if !c.HasCookie() {
		return nil, ErrNotLoggedIn
	}
	u := fmt.Sprintf("%s/account/info?fr=pc&platform=pc", c.panBaseURL)
	var resp struct {
		Success bool   `json:"success"`
		Code    string `json:"code"`
		Message string `json:"message"`
		Data    struct {
			Nickname  string `json:"nickname"`
			AvatarURI string `json:"avatarUri"`
			MobileKPS string `json:"mobilekps"`
			UID       string `json:"uid"`
			MemberID  string `json:"member_id"`
		} `json:"data"`
	}
	if err := c.doRawJSON(ctx, "获取账号信息", http.MethodGet, u, nil, false, &resp); err != nil {
		return nil, err
	}
	if !resp.Success && strings.TrimSpace(resp.Data.Nickname) == "" {
		msg := resp.Message
		if msg == "" {
			msg = "登录状态无效或 Cookie 已失效"
		}
		return nil, &QuarkError{
			Operation: "获取账号信息",
			Message:   msg,
			Err:       ErrCookieExpired,
		}
	}

	nick := strings.TrimSpace(resp.Data.Nickname)
	uid := strings.TrimSpace(resp.Data.UID)
	if uid == "" {
		uid = strings.TrimSpace(resp.Data.MemberID)
	}
	if uid == "" {
		uid = deriveStableUID(nick, resp.Data.MobileKPS)
	}

	return &AccountInfo{
		UID:       uid,
		Nickname:  nick,
		AvatarURI: strings.TrimSpace(resp.Data.AvatarURI),
	}, nil
}

// deriveStableUID 当接口未返回显式数字 UID 时，基于账号昵称生成稳定的 12 位十六进制标识
func deriveStableUID(nickname, salt string) string {
	seed := strings.TrimSpace(nickname)
	if seed == "" {
		seed = strings.TrimSpace(salt)
	}
	if seed == "" {
		seed = "quark-user"
	}
	sum := sha256.Sum256([]byte("kkpan:" + seed))
	return hex.EncodeToString(sum[:6])
}

// GetMemberQuota 查询夸克网盘已用空间、总容量及会员等级
func (c *Client) GetMemberQuota(ctx context.Context) (*MemberQuota, error) {
	q := url.Values{}
	q.Set("fetch_subscribe", "true")
	q.Set("_ch", "home")

	var data struct {
		MemberType    string `json:"member_type"`
		UseCapacity   int64  `json:"use_capacity"`
		TotalCapacity int64  `json:"total_capacity"`
	}
	if err := c.doDriveJSON(ctx, "查询网盘容量与会员信息", http.MethodGet, "/1/clouddrive/member", q, nil, &data); err != nil {
		return nil, err
	}

	mt := strings.TrimSpace(data.MemberType)
	upper := strings.ToUpper(mt)
	isSVIP := strings.Contains(upper, "SVIP") || strings.Contains(upper, "SUPER")
	isVIP := isSVIP || (strings.Contains(upper, "VIP") && upper != "NORMAL")

	if mt == "" {
		mt = "NORMAL"
	}
	return &MemberQuota{
		UseCapacity:   data.UseCapacity,
		TotalCapacity: data.TotalCapacity,
		MemberType:    mt,
		IsVIP:         isVIP,
		IsSVIP:        isSVIP,
	}, nil
}

// GetQRToken 向 uop.quark.cn 申请扫码登录临时 token 并构造移动端扫码 URL
func (c *Client) GetQRToken(ctx context.Context) (*QRCodeSession, error) {
	reqID, err := generateUUIDv4()
	if err != nil {
		return nil, err
	}

	q := url.Values{}
	q.Set("client_id", qrClientID)
	q.Set("v", qrVersion)
	q.Set("request_id", reqID)

	u := fmt.Sprintf("%s/cas/ajax/getTokenForQrcodeLogin?%s", c.uopBaseURL, q.Encode())
	var resp struct {
		Status  int    `json:"status"`
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			Members struct {
				Token string `json:"token"`
			} `json:"members"`
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := c.doRawJSON(ctx, "申请扫码登录令牌", http.MethodGet, u, nil, false, &resp); err != nil {
		return nil, err
	}

	token := strings.TrimSpace(resp.Data.Members.Token)
	if token == "" {
		token = strings.TrimSpace(resp.Data.Token)
	}
	if resp.Status != 2000000 || token == "" {
		return nil, classifyError("申请扫码登录令牌", 200, resp.Status, resp.Code, resp.Message)
	}

	scanQ := url.Values{}
	scanQ.Set("token", token)
	scanQ.Set("client_id", qrClientID)
	scanQ.Set("ssb", "weblogin")
	scanQ.Set("uc_param_str", "")
	scanQ.Set("uc_biz_str", "S:custom|OPT:SAREA@0|OPT:IMMERSIVE@1|OPT:BACK_BTN_STYLE@0")

	return &QRCodeSession{
		Token:     token,
		RequestID: reqID,
		QRURL:     qrScanBaseURL + "?" + scanQ.Encode(),
	}, nil
}

// PollQRStatus 轮询二维码扫码状态，返回等待中、已确认（含 service_ticket）或已过期
func (c *Client) PollQRStatus(ctx context.Context, token, requestID string) (*QRPollResult, error) {
	q := url.Values{}
	q.Set("client_id", qrClientID)
	q.Set("v", qrVersion)
	q.Set("token", token)
	q.Set("request_id", requestID)

	u := fmt.Sprintf("%s/cas/ajax/getServiceTicketByQrcodeToken?%s", c.uopBaseURL, q.Encode())
	var resp struct {
		Status  int    `json:"status"`
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			Members struct {
				ServiceTicket string `json:"service_ticket"`
			} `json:"members"`
			ServiceTicket string `json:"service_ticket"`
		} `json:"data"`
	}
	if err := c.doRawJSON(ctx, "轮询扫码状态", http.MethodGet, u, nil, false, &resp); err != nil {
		return nil, err
	}

	switch resp.Status {
	case 2000000:
		st := strings.TrimSpace(resp.Data.Members.ServiceTicket)
		if st == "" {
			st = strings.TrimSpace(resp.Data.ServiceTicket)
		}
		if st == "" {
			return nil, fmt.Errorf("扫码成功但未返回 service_ticket")
		}
		return &QRPollResult{
			State:         QRStateConfirmed,
			ServiceTicket: st,
			Message:       resp.Message,
		}, nil
	case 50004001:
		return &QRPollResult{
			State:   QRStateWaiting,
			Message: resp.Message,
		}, nil
	case 50004002:
		return &QRPollResult{
			State:   QRStateExpired,
			Message: resp.Message,
		}, ErrQRExpired
	default:
		// 兼容其他未知非终态状态码：若 message 含过期则视为过期，否则视为异常
		if strings.Contains(resp.Message, "过期") || strings.Contains(strings.ToLower(resp.Message), "expired") {
			return &QRPollResult{State: QRStateExpired, Message: resp.Message}, ErrQRExpired
		}
		return nil, classifyError("轮询扫码状态", 200, resp.Status, resp.Code, resp.Message)
	}
}

// ExchangeServiceTicket 使用扫码确认后的 service_ticket 换取 pan.quark.cn 与 drive-pc 会话 Cookie，
// 并返回完整账号信息与脱敏外可持久化的 Cookie 字符串。
func (c *Client) ExchangeServiceTicket(ctx context.Context, serviceTicket string) (*AccountInfo, string, error) {
	if strings.TrimSpace(serviceTicket) == "" {
		return nil, "", fmt.Errorf("service_ticket 不能为空")
	}

	// 使用隔离的临时客户端完成换票并收集跨子域 Set-Cookie，避免污染原客户端状态
	jar, _ := cookiejar.New(nil)
	tmpClient := &Client{
		driveBaseURL: c.driveBaseURL,
		panBaseURL:   c.panBaseURL,
		uopBaseURL:   c.uopBaseURL,
		userAgent:    c.userAgent,
		httpClient: &http.Client{
			Timeout:   c.httpClient.Timeout,
			Transport: c.httpClient.Transport,
			Jar:       jar,
		},
		cookies: make(map[string]string),
	}

	q := url.Values{}
	q.Set("st", serviceTicket)
	q.Set("lw", "scan")
	u := fmt.Sprintf("%s/account/info?%s", tmpClient.panBaseURL, q.Encode())

	var infoResp struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
		Data    struct {
			Nickname  string `json:"nickname"`
			AvatarURI string `json:"avatarUri"`
			MobileKPS string `json:"mobilekps"`
			UID       string `json:"uid"`
			MemberID  string `json:"member_id"`
		} `json:"data"`
	}
	if err := tmpClient.doRawJSON(ctx, "换取登录会话", http.MethodGet, u, nil, false, &infoResp); err != nil {
		return nil, "", err
	}

	// 同时从 cookiejar 收集作用域内全部 Cookie，补充合并到 CookieHeader
	for _, baseStr := range []string{tmpClient.panBaseURL, tmpClient.driveBaseURL} {
		if parsed, err := url.Parse(baseStr); err == nil {
			for _, ck := range jar.Cookies(parsed) {
				if ck.Name != "" && ck.Value != "" {
					tmpClient.cookieMu.Lock()
					tmpClient.cookies[ck.Name] = ck.Value
					tmpClient.cookieMu.Unlock()
				}
			}
		}
	}

	if !tmpClient.HasCookie() {
		return nil, "", fmt.Errorf("换取登录会话失败: 服务端未下发有效会话 Cookie")
	}

	// 预热 drive-pc 会话，获取可能下发的 __puus 续期 Cookie
	_, _ = tmpClient.GetMemberQuota(ctx)

	nick := strings.TrimSpace(infoResp.Data.Nickname)
	uid := strings.TrimSpace(infoResp.Data.UID)
	if uid == "" {
		uid = strings.TrimSpace(infoResp.Data.MemberID)
	}
	if uid == "" {
		uid = deriveStableUID(nick, infoResp.Data.MobileKPS)
	}

	cookieHeader := tmpClient.CookieHeader()
	c.SetCookie(cookieHeader)

	return &AccountInfo{
		UID:       uid,
		Nickname:  nick,
		AvatarURI: strings.TrimSpace(infoResp.Data.AvatarURI),
	}, cookieHeader, nil
}

// WaitQRLoginResult 是扫码登录轮询的完整结果
type WaitQRLoginResult struct {
	Account *AccountInfo
	Cookie  string
}

// RunQRLoginLoop 在给定上下文中轮询扫码状态直到确认、过期或取消
func (c *Client) RunQRLoginLoop(ctx context.Context, session *QRCodeSession, pollInterval time.Duration) (*WaitQRLoginResult, error) {
	if session == nil {
		return nil, fmt.Errorf("扫码会话不能为空")
	}
	if pollInterval <= 0 {
		pollInterval = 2 * time.Second
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			if ctx.Err() == context.Canceled {
				return nil, ErrLoginCanceled
			}
			return nil, fmt.Errorf("等待扫码超时: %w", ctx.Err())
		case <-ticker.C:
			res, err := c.PollQRStatus(ctx, session.Token, session.RequestID)
			if err != nil {
				return nil, err
			}
			switch res.State {
			case QRStateConfirmed:
				info, cookieStr, err := c.ExchangeServiceTicket(ctx, res.ServiceTicket)
				if err != nil {
					return nil, err
				}
				return &WaitQRLoginResult{
					Account: info,
					Cookie:  cookieStr,
				}, nil
			case QRStateExpired:
				return nil, ErrQRExpired
			case QRStateWaiting:
				continue
			}
		}
	}
}

// Ensure json import stays used when needed by future expansions
var _ = json.Marshal
