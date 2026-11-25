package quarkpan

import (
	"errors"
	"fmt"
	"strings"
)

var (
	// ErrNotLoggedIn 表示当前尚未配置登录凭证
	ErrNotLoggedIn = errors.New("未登录夸克网盘")
	// ErrCookieExpired 表示当前登录态（Cookie）已失效或被风控拦截
	ErrCookieExpired = errors.New("夸克网盘登录凭证已过期或无效")
	// ErrNotFound 表示目标网盘路径或 FID 不存在
	ErrNotFound = errors.New("目标文件或目录不存在")
	// ErrAlreadyExists 表示目标位置已存在同名文件或目录
	ErrAlreadyExists = errors.New("目标文件或目录已存在")
	// ErrQuotaExceeded 表示网盘存储空间不足
	ErrQuotaExceeded = errors.New("网盘存储空间不足")
	// ErrRateLimited 表示触发服务端限流或频率限制
	ErrRateLimited = errors.New("请求过于频繁，已触发夸克网盘限流")
	// ErrTaskFailed 表示云端异步任务执行失败
	ErrTaskFailed = errors.New("云端异步任务执行失败")
	// ErrQRExpired 表示扫码登录二维码已过期
	ErrQRExpired = errors.New("登录二维码已过期")
	// ErrLoginCanceled 表示用户主动取消登录流程
	ErrLoginCanceled = errors.New("已取消登录")
	// ErrShareInvalid 表示分享链接无效、已过期或已被取消
	ErrShareInvalid = errors.New("分享链接无效、已过期或已被取消")
	// ErrSharePasscodeInvalid 表示分享提取码错误或需要提供提取码
	ErrSharePasscodeInvalid = errors.New("分享提取码错误或需要提供提取码")
)

// QuarkError 封装夸克网盘 HTTP/业务接口返回的结构化错误信息
type QuarkError struct {
	Operation string
	Status    int
	Code      int
	Message   string
	Err       error
}

// Error 返回简洁清晰的中文错误描述
func (e *QuarkError) Error() string {
	if e == nil {
		return ""
	}
	var parts []string
	if e.Operation != "" {
		parts = append(parts, e.Operation)
	}
	if e.Status != 0 || e.Code != 0 {
		parts = append(parts, fmt.Sprintf("status=%d code=%d", e.Status, e.Code))
	}
	if e.Message != "" {
		parts = append(parts, e.Message)
	}
	if e.Err != nil && !strings.Contains(e.Message, e.Err.Error()) {
		parts = append(parts, e.Err.Error())
	}
	if len(parts) == 0 {
		return "夸克网盘请求失败"
	}
	return strings.Join(parts, ": ")
}

// Unwrap 支持 errors.Is 与 errors.As 解包底层哨兵错误
func (e *QuarkError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// classifyError 根据 HTTP 状态码、业务 code 和消息映射常见哨兵错误，方便上层给出精确排错建议
func classifyError(op string, httpStatus, status, code int, message string) error {
	lower := strings.ToLower(message)
	var sentinel error

	switch {
	case code == 41008 || code == 41010 || code == 41011 ||
		strings.Contains(message, "提取码") || strings.Contains(lower, "passcode"):
		sentinel = ErrSharePasscodeInvalid
	case code == 41004 || code == 41006 || code == 41009 ||
		strings.Contains(message, "分享已失效") || strings.Contains(message, "分享不存在") || strings.Contains(message, "已取消分享"):
		sentinel = ErrShareInvalid
	case httpStatus == 401 || httpStatus == 403 || code == 31001 || code == 20001 ||
		strings.Contains(lower, "need login") || strings.Contains(lower, "unauthorized") ||
		strings.Contains(message, "未登录") || strings.Contains(message, "登录失效"):
		sentinel = ErrCookieExpired
	case httpStatus == 404 || code == 23008 || code == 23015 ||
		strings.Contains(lower, "not found") || strings.Contains(lower, "not exist") ||
		strings.Contains(message, "不存在"):
		sentinel = ErrNotFound
	case code == 23003 || strings.Contains(lower, "already exist") || strings.Contains(message, "已存在"):
		sentinel = ErrAlreadyExists
	case code == 23006 || strings.Contains(lower, "capacity") || strings.Contains(lower, "quota") ||
		strings.Contains(message, "空间不足") || strings.Contains(message, "容量"):
		sentinel = ErrQuotaExceeded
	case httpStatus == 429 || code == 23018 || strings.Contains(lower, "too many") ||
		strings.Contains(lower, "rate limit") || strings.Contains(message, "频繁"):
		sentinel = ErrRateLimited
	}

	effStatus := status
	if effStatus == 0 {
		effStatus = httpStatus
	}

	return &QuarkError{
		Operation: op,
		Status:    effStatus,
		Code:      code,
		Message:   strings.TrimSpace(message),
		Err:       sentinel,
	}
}
