package quarkpan

import (
	"context"
	"fmt"
	"path"
	"strings"
	"sync"
	"time"
)

const (
	// RootFID 夸克网盘根目录的固定文件 ID
	RootFID = "0"
	// FIDPrefix 用于直接按 FID 指定目标的 CLI 语法前缀（规避同目录重名文件歧义）
	FIDPrefix = "fid:"
)

type dirCacheEntry struct {
	items     []FileItem
	nameToFID map[string]string
	expiresAt time.Time
}

// PathResolver 负责夸克网盘虚拟路径（POSIX 风格）与底层 FID 之间的双向映射与短时缓存
type PathResolver struct {
	client *Client
	ttl    time.Duration

	mu       sync.RWMutex
	dirCache map[string]*dirCacheEntry
	fidToDir map[string]string
}

// NewPathResolver 创建路径解析器实例
func NewPathResolver(client *Client, ttl time.Duration) *PathResolver {
	if ttl <= 0 {
		ttl = 60 * time.Second
	}
	return &PathResolver{
		client:   client,
		ttl:      ttl,
		dirCache: make(map[string]*dirCacheEntry),
		fidToDir: map[string]string{RootFID: "/"},
	}
}

// NormalizePath 将用户输入（支持 Windows 反斜杠、相对路径、.. 与 fid: 直连）规范化为网盘路径或 fid: 前缀
func NormalizePath(workdir, input string) string {
	raw := strings.TrimSpace(input)
	if strings.HasPrefix(strings.ToLower(raw), FIDPrefix) {
		return FIDPrefix + strings.TrimSpace(raw[len(FIDPrefix):])
	}

	raw = strings.ReplaceAll(raw, "\\", "/")
	wd := strings.ReplaceAll(strings.TrimSpace(workdir), "\\", "/")
	if wd == "" || !strings.HasPrefix(wd, "/") {
		wd = "/"
	}

	if raw == "" {
		return path.Clean(wd)
	}
	if !strings.HasPrefix(raw, "/") {
		raw = path.Join(wd, raw)
	}
	cleaned := path.Clean(raw)
	if !strings.HasPrefix(cleaned, "/") {
		cleaned = "/" + cleaned
	}
	return cleaned
}

// ParseFIDPrefix 检查字符串是否为 fid:<id> 直连格式
func ParseFIDPrefix(input string) (string, bool) {
	trimmed := strings.TrimSpace(input)
	if len(trimmed) > len(FIDPrefix) && strings.EqualFold(trimmed[:len(FIDPrefix)], FIDPrefix) {
		id := strings.TrimSpace(trimmed[len(FIDPrefix):])
		if id != "" {
			return id, true
		}
	}
	return "", false
}

// InvalidateDir 使指定父目录 FID 的缓存条目失效
func (r *PathResolver) InvalidateDir(pdirFID string) {
	if r == nil || pdirFID == "" {
		return
	}
	r.mu.Lock()
	delete(r.dirCache, pdirFID)
	r.mu.Unlock()
}

// InvalidateAll 清空所有路径与目录缓存
func (r *PathResolver) InvalidateAll() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.dirCache = make(map[string]*dirCacheEntry)
	r.fidToDir = map[string]string{RootFID: "/"}
	r.mu.Unlock()
}

// cacheDirItems 记录某目录 FID 下的子项，构建按名称索引表
func (r *PathResolver) cacheDirItems(pdirFID, dirPath string, items []FileItem) {
	if r == nil || pdirFID == "" {
		return
	}
	copied := make([]FileItem, len(items))
	nameMap := make(map[string]string, len(items))
	for i, it := range items {
		copied[i] = it
		// 若同目录存在极少见的重名项，保留首次出现的排序首位
		if _, exists := nameMap[it.FileName]; !exists {
			nameMap[it.FileName] = it.FID
		}
	}

	r.mu.Lock()
	if dirPath != "" {
		r.fidToDir[pdirFID] = dirPath
	}
	r.dirCache[pdirFID] = &dirCacheEntry{
		items:     copied,
		nameToFID: nameMap,
		expiresAt: time.Now().Add(r.ttl),
	}
	r.mu.Unlock()
}

func (r *PathResolver) getCachedChildren(pdirFID string) ([]FileItem, bool) {
	r.mu.RLock()
	entry, ok := r.dirCache[pdirFID]
	r.mu.RUnlock()
	if !ok || time.Now().After(entry.expiresAt) {
		return nil, false
	}
	out := make([]FileItem, len(entry.items))
	copy(out, entry.items)
	return out, true
}

// ResolvePath 将绝对网盘路径或 fid:<id> 解析为对应 FileItem（根目录返回合成根目录节点）
func (r *PathResolver) ResolvePath(ctx context.Context, inputPath string) (*FileItem, error) {
	if fid, ok := ParseFIDPrefix(inputPath); ok {
		if fid == RootFID {
			return &FileItem{
				FID:      RootFID,
				PDirFID:  "",
				FileName: "/",
				IsDir:    true,
				FullPath: "/",
			}, nil
		}
		return &FileItem{
			FID:      fid,
			FileName: fid,
			FullPath: FIDPrefix + fid,
			IsDir:    false,
		}, nil
	}

	cleanPath := NormalizePath("/", inputPath)
	if cleanPath == "/" {
		return &FileItem{
			FID:      RootFID,
			PDirFID:  "",
			FileName: "/",
			IsDir:    true,
			FullPath: "/",
		}, nil
	}

	segments := strings.Split(strings.TrimPrefix(cleanPath, "/"), "/")
	curFID := RootFID
	curPath := ""

	var resolved *FileItem
	for idx, seg := range segments {
		if seg == "" {
			continue
		}
		curPath += "/" + seg
		items, ok := r.getCachedChildren(curFID)
		if !ok {
			var err error
			items, err = r.client.ListDirByFID(ctx, curFID, &ListOptions{ForceRefresh: true})
			if err != nil {
				return nil, err
			}
		}

		var matched *FileItem
		for i := range items {
			if items[i].FileName == seg {
				it := items[i]
				it.FullPath = curPath
				matched = &it
				break
			}
		}
		if matched == nil {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, cleanPath)
		}
		if idx < len(segments)-1 && !matched.IsDir {
			return nil, fmt.Errorf("%w: %s 不是目录", ErrNotFound, curPath)
		}
		resolved = matched
		curFID = matched.FID
	}

	if resolved == nil {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, cleanPath)
	}
	return resolved, nil
}

// ResolveDirFID 解析必须为目录的路径并返回其 FID
func (r *PathResolver) ResolveDirFID(ctx context.Context, inputPath string) (string, string, error) {
	if fid, ok := ParseFIDPrefix(inputPath); ok {
		return fid, FIDPrefix + fid, nil
	}
	item, err := r.ResolvePath(ctx, inputPath)
	if err != nil {
		return "", "", err
	}
	if !item.IsDir {
		return "", "", fmt.Errorf("目标不是目录: %s", item.FullPath)
	}
	return item.FID, item.FullPath, nil
}
