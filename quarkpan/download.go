package quarkpan

import (
	"context"
	"fmt"
	"net/http"
)

// GetDownloadLinks 批量获取指定 FID 列表的临时 CDN 下载直链
func (c *Client) GetDownloadLinks(ctx context.Context, fids []string) ([]DownloadLink, error) {
	if len(fids) == 0 {
		return nil, fmt.Errorf("未指定需要获取直链的文件 FID")
	}
	req := map[string]interface{}{
		"fids": fids,
	}
	var links []DownloadLink
	if err := c.doDriveJSON(ctx, "获取文件下载直链", http.MethodPost, "/1/clouddrive/file/download", nil, req, &links); err != nil {
		return nil, err
	}
	if len(links) == 0 {
		return nil, fmt.Errorf("%w: 服务端未返回任何下载链接", ErrNotFound)
	}
	return links, nil
}
