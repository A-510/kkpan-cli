package quarkpan

import (
	"encoding/json"
	"strings"
)

// FileItem 描述夸克网盘中的文件或目录元数据
type FileItem struct {
	FID        string `json:"fid"`
	PDirFID    string `json:"pdir_fid"`
	FileName   string `json:"file_name"`
	Size       int64  `json:"size"`
	IsDir      bool   `json:"dir"`
	FormatType string `json:"format_type,omitempty"`
	Category   int    `json:"category,omitempty"`
	MD5        string `json:"md5,omitempty"`
	SHA1       string `json:"sha1,omitempty"`
	CreatedAt  int64  `json:"created_at"`
	UpdatedAt  int64  `json:"updated_at"`
	FullPath   string `json:"full_path,omitempty"`
}

// AccountInfo 描述当前登录的夸克账号基本信息（不包含 Cookie 等敏感凭证）
type AccountInfo struct {
	UID       string `json:"uid"`
	Nickname  string `json:"nickname"`
	AvatarURI string `json:"avatar_uri,omitempty"`
}

// MemberQuota 描述夸克网盘容量与会员等级信息
type MemberQuota struct {
	UseCapacity   int64  `json:"use_capacity"`
	TotalCapacity int64  `json:"total_capacity"`
	MemberType    string `json:"member_type"`
	IsVIP         bool   `json:"is_vip"`
	IsSVIP        bool   `json:"is_svip"`
}

// DownloadLink 描述文件直链及元数据
type DownloadLink struct {
	FID         string `json:"fid"`
	FileName    string `json:"file_name"`
	Size        int64  `json:"size"`
	MD5         string `json:"md5,omitempty"`
	UpdatedAt   int64  `json:"updated_at,omitempty"`
	DownloadURL string `json:"download_url"`
	BackupURL   string `json:"backup_url,omitempty"`
}

// CandidateURLs 返回去重后的非空下载候选地址（主直链优先，备选直链兜底）
func (l DownloadLink) CandidateURLs() []string {
	seen := make(map[string]struct{}, 2)
	urls := make([]string, 0, 2)
	for _, raw := range []string{l.DownloadURL, l.BackupURL} {
		u := strings.TrimSpace(raw)
		if u == "" {
			continue
		}
		if _, ok := seen[u]; ok {
			continue
		}
		seen[u] = struct{}{}
		urls = append(urls, u)
	}
	return urls
}

// TaskSaveAs 描述异步转存任务完成后的顶层落盘 FID 列表
type TaskSaveAs struct {
	SaveAsTopFIDs []string `json:"save_as_top_fids,omitempty"`
}

// TaskStatus 描述云端异步任务轮询状态
type TaskStatus struct {
	TaskID        string      `json:"task_id"`
	Status        int         `json:"status"`
	TaskTitle     string      `json:"task_title,omitempty"`
	Message       string      `json:"message,omitempty"`
	ShareID       string      `json:"share_id,omitempty"`
	SaveAs        *TaskSaveAs `json:"save_as,omitempty"`
	SaveAsTopFIDs []string    `json:"save_as_top_fids,omitempty"`
}

// TopFIDs 返回转存任务完成后生成的新顶层 FID 列表（优先读取嵌套 save_as 字段，兼容平铺字段）
func (t *TaskStatus) TopFIDs() []string {
	if t == nil {
		return nil
	}
	if t.SaveAs != nil && len(t.SaveAs.SaveAsTopFIDs) > 0 {
		return t.SaveAs.SaveAsTopFIDs
	}
	return t.SaveAsTopFIDs
}

// UploadPreResponse 描述 pre-upload 接口返回的任务、秒传与 OSS 上传参数
type UploadPreResponse struct {
	TaskID      string          `json:"task_id"`
	UploadID    string          `json:"upload_id"`
	ObjKey      string          `json:"obj_key"`
	UploadURL   string          `json:"upload_url"`
	Bucket      string          `json:"bucket"`
	AuthInfo    string          `json:"auth_info"`
	CallbackRaw json.RawMessage `json:"callback"`
	Finish      bool            `json:"finish"`
	FID         string          `json:"fid"`
	PartSize    int64           `json:"part_size"`
}

// UploadPart 描述已上传的 OSS 分片序号与 ETag
type UploadPart struct {
	PartNumber int    `json:"part_number"`
	ETag       string `json:"etag"`
}

// ListOptions 控制目录列举时的排序与缓存行为
type ListOptions struct {
	SortField    string
	SortOrder    string
	ForceRefresh bool
}

// QRCodeSession 描述一次终端扫码登录会话
type QRCodeSession struct {
	Token     string `json:"token"`
	RequestID string `json:"request_id"`
	QRURL     string `json:"qr_url"`
}

// QRPollState 表示轮询扫码状态的结果类别
type QRPollState int

const (
	// QRStateWaiting 表示等待用户扫码或尚未点击确认
	QRStateWaiting QRPollState = iota
	// QRStateConfirmed 表示用户已在移动端确认登录，已获取 service_ticket
	QRStateConfirmed
	// QRStateExpired 表示二维码已过期，需要重新生成
	QRStateExpired
)

// QRPollResult 描述单次二维码状态轮询结果
type QRPollResult struct {
	State         QRPollState
	ServiceTicket string
	Message       string
}

// ShareTokenInfo 描述验证分享链接与提取码后返回的访问令牌信息
type ShareTokenInfo struct {
	PWDID       string `json:"pwd_id"`
	SToken      string `json:"stoken"`
	Title       string `json:"title,omitempty"`
	AuthorName  string `json:"author_name,omitempty"`
	ShareType   int    `json:"share_type,omitempty"`
	ExpiredAt   int64  `json:"expired_at,omitempty"`
	ExpiredType int    `json:"expired_type,omitempty"`
}

// ShareInfo 描述分享页面基础元信息
type ShareInfo struct {
	ShareID     string `json:"share_id,omitempty"`
	PWDID       string `json:"pwd_id"`
	Title       string `json:"title"`
	ShareURL    string `json:"share_url,omitempty"`
	Passcode    string `json:"passcode,omitempty"`
	ExpiredAt   int64  `json:"expired_at,omitempty"`
	ExpiredType int    `json:"expired_type,omitempty"`
	Status      int    `json:"status,omitempty"`
	CreatedAt   int64  `json:"created_at,omitempty"`
}

// ShareFileItem 描述分享链接内的文件或目录条目（附带 share_fid_token）
type ShareFileItem struct {
	FID           string `json:"fid"`
	PDirFID       string `json:"pdir_fid"`
	FileName      string `json:"file_name"`
	Size          int64  `json:"size"`
	IsDir         bool   `json:"dir"`
	FormatType    string `json:"format_type,omitempty"`
	ShareFIDToken string `json:"share_fid_token"`
	UpdatedAt     int64  `json:"updated_at"`
	CreatedAt     int64  `json:"created_at"`
}

// ShareDetailResult 描述分享列表查询结果
type ShareDetailResult struct {
	Share *ShareInfo      `json:"share,omitempty"`
	Items []ShareFileItem `json:"items"`
	Total int             `json:"total"`
}

// ShareSaveRequest 描述转存分享文件到个人网盘的请求参数
type ShareSaveRequest struct {
	PWDID     string   `json:"pwd_id"`
	SToken    string   `json:"stoken"`
	PDirFID   string   `json:"pdir_fid"`
	ToPDirFID string   `json:"to_pdir_fid"`
	FIDs      []string `json:"fid_list"`
	FIDTokens []string `json:"fid_token_list"`
}

// ShareSaveResult 描述转存完成后的结果摘要
type ShareSaveResult struct {
	TaskID       string   `json:"task_id,omitempty"`
	SavedTopFIDs []string `json:"saved_top_fids,omitempty"`
	ToPDirFID    string   `json:"to_pdir_fid"`
}

// CreateShareOptions 描述创建网盘分享链接的选项
type CreateShareOptions struct {
	FIDs       []string `json:"fid_list"`
	Title      string   `json:"title"`
	Passcode   string   `json:"passcode,omitempty"`
	PeriodDays int      `json:"period_days"`
}

// CreateShareResult 描述新建分享并换取短链后的结果
type CreateShareResult struct {
	ShareID     string `json:"share_id"`
	PWDID       string `json:"pwd_id"`
	Title       string `json:"title"`
	ShareURL    string `json:"share_url"`
	Passcode    string `json:"passcode,omitempty"`
	URLType     int    `json:"url_type"`
	ExpiredType int    `json:"expired_type"`
	ExpiredAt   int64  `json:"expired_at,omitempty"`
}

// MyShareItem 描述用户“我的分享”列表中的单条分享记录
type MyShareItem struct {
	ShareID     string `json:"share_id"`
	PWDID       string `json:"pwd_id"`
	Title       string `json:"title"`
	ShareURL    string `json:"share_url"`
	Passcode    string `json:"passcode,omitempty"`
	FirstFID    string `json:"first_fid,omitempty"`
	ExpiredType int    `json:"expired_type"`
	ExpiredAt   int64  `json:"expired_at"`
	ClickPV     int64  `json:"click_pv"`
	SavePV      int64  `json:"save_pv"`
	DownloadPV  int64  `json:"download_pv"`
	Status      int    `json:"status"`
	CreatedAt   int64  `json:"created_at"`
}

// MyShareListResult 描述我的分享分页列表响应
type MyShareListResult struct {
	Items []MyShareItem `json:"items"`
	Total int           `json:"total"`
	Page  int           `json:"page"`
	Size  int           `json:"size"`
}

// RecycleItem 描述回收站中的单条文件或目录记录
type RecycleItem struct {
	RecordID   string `json:"record_id"`
	FID        string `json:"fid"`
	PDirFID    string `json:"pdir_fid,omitempty"`
	FileName   string `json:"file_name"`
	Size       int64  `json:"size"`
	IsDir      bool   `json:"dir"`
	FormatType string `json:"format_type,omitempty"`
	DeletedAt  int64  `json:"deleted_at"`
	ExpiredAt  int64  `json:"expired_at"`
}

// RecycleListResult 描述回收站分页列表响应
type RecycleListResult struct {
	Items []RecycleItem `json:"items"`
	Total int           `json:"total"`
	Page  int           `json:"page"`
	Size  int           `json:"size"`
}
