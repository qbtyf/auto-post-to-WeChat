// Package wechat 实现微信公众号「素材 + 草稿」API 客户端。
//
// 用到的接口（与 Python 版一致）：
//  1. GET  /cgi-bin/token                    → 获取 access_token
//  2. POST /cgi-bin/media/uploadimg          → 上传正文图片（返回 mmbiz URL，正文专用）
//  3. POST /cgi-bin/material/add_material    → 上传封面永久素材（返回 media_id）
//  4. POST /cgi-bin/draft/add                → 新增草稿
//
// 所有请求都走 tunnel 提供的本地 SOCKS5 代理（HTTP client 由外部注入）。
package wechat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const apiBase = "https://api.weixin.qq.com"

// Client 微信公众号 API 客户端。
type Client struct {
	appID     string
	appSecret string
	hc        *http.Client // 由外部注入：已配置走 SOCKS5 隧道的客户端
	logf      func(format string, args ...any) // 日志回调（写进 Fyne 窗口日志区）
	token     string
	tokenExp  time.Time
}

// NewClient 创建客户端并立即获取 access_token。
func NewClient(appID, appSecret string, hc *http.Client, logf func(string, ...any)) (*Client, error) {
	c := &Client{appID: appID, appSecret: appSecret, hc: hc, logf: logf}
	if err := c.refreshToken(); err != nil {
		return nil, err
	}
	return c, nil
}

// refreshToken 获取新的 access_token（有效期 7200 秒，本工具单次发布用一次即可）。
func (c *Client) refreshToken() error {
	url := fmt.Sprintf("%s/cgi-bin/token?grant_type=client_credential&appid=%s&secret=%s",
		apiBase, c.appID, c.appSecret)
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		ErrCode     int    `json:"errcode"`
		ErrMsg      string `json:"errmsg"`
	}
	if err := c.getJSON(url, &out); err != nil {
		return err
	}
	if out.AccessToken == "" {
		return fmt.Errorf("获取 access_token 失败: errcode=%d errmsg=%s", out.ErrCode, out.ErrMsg)
	}
	c.token = out.AccessToken
	c.tokenExp = time.Now().Add(time.Duration(out.ExpiresIn-60) * time.Second)
	c.logf("✅ access_token 获取成功（有效期 %d 秒）", out.ExpiresIn)
	return nil
}

// tokenValid 返回当前 token 是否仍然有效。
func (c *Client) tokenValid() bool {
	return c.token != "" && time.Now().Before(c.tokenExp)
}

// getJSON 发 GET 请求并解析 JSON 响应。
func (c *Client) getJSON(url string, out any) error {
	resp, err := c.hc.Get(url)
	if err != nil {
		return fmt.Errorf("请求失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return json.Unmarshal(body, out)
}

// uploadFile 通用 multipart 文件上传（uploadimg / add_material 共用）。
// 参数：apiPath 接口路径；fieldName 表单字段名（都是 media）；filePath 本地文件。
// 返回：响应 JSON 的原始 map（不同接口字段不同，调用方自行取）。
func (c *Client) uploadFile(apiPath, fieldName, filePath string) (map[string]any, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("读取文件失败 %s: %w", filePath, err)
	}

	// 组 multipart/form-data（微信要求的文件表单格式）
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile(fieldName, filepath.Base(filePath))
	if err != nil {
		return nil, err
	}
	if _, err := fw.Write(data); err != nil {
		return nil, err
	}
	_ = w.Close()

	// 注意：apiPath 可能自带 query（如 "?type=image"），access_token 必须用正确的分隔符拼接，
	// 否则出现两个 "?" 导致微信收不到 access_token（errcode=41001）。
	sep := "?"
	if strings.Contains(apiPath, "?") {
		sep = "&"
	}
	url := fmt.Sprintf("%s%s%saccess_token=%s", apiBase, apiPath, sep, c.token)
	req, err := http.NewRequest("POST", url, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("上传请求失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("响应解析失败: %s", string(body))
	}
	return out, nil
}

// UploadContentImage 规则内接口 2：上传正文图片，返回 mmbiz.qpic.cn 的 URL。
// 正文 HTML 里的 <img> 只认这个域名，本地图片必须先走这一步并替换。
func (c *Client) UploadContentImage(localPath string) (string, error) {
	out, err := c.uploadFile("/cgi-bin/media/uploadimg", "media", localPath)
	if err != nil {
		return "", err
	}
	if u, ok := out["url"].(string); ok && u != "" {
		return u, nil
	}
	return "", fmt.Errorf("uploadimg 失败: %v", out)
}

// UploadCover 接口 3：上传封面为永久素材，返回 thumb_media_id。
// 注意：封面必须走 add_material（uploadimg 的 URL 不能当封面）。
func (c *Client) UploadCover(localPath string) (string, error) {
	out, err := c.uploadFile("/cgi-bin/material/add_material?type=image", "media", localPath)
	if err != nil {
		return "", err
	}
	if mid, ok := out["media_id"].(string); ok && mid != "" {
		return mid, nil
	}
	return "", fmt.Errorf("add_material 失败: %v", out)
}

// ReplaceLocalImages 把正文 HTML 里的本地 <img src="..."> 批量上传并替换为微信 URL。
// 返回处理后的 HTML 与替换计数。
func (c *Client) ReplaceLocalImages(htmlStr, mdDir string) (string, int, error) {
	var replaced int
	var failErr error

	// 匹配 <img ... src="..." ...>
	result := imgSrcRe.ReplaceAllStringFunc(htmlStr, func(imgTag string) string {
		groups := imgSrcRe.FindStringSubmatch(imgTag)
		src := groups[1]
		if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") {
			return imgTag // 已是网络图，不动
		}
		// 相对路径 → 相对 MD 文件所在目录解析
		local := src
		if !filepath.IsAbs(local) {
			local = filepath.Join(mdDir, local)
		}
		if _, err := os.Stat(local); err != nil {
			failErr = fmt.Errorf("正文图片不存在: %s", local)
			return imgTag
		}
		url, err := c.UploadContentImage(local)
		if err != nil {
			failErr = fmt.Errorf("正文图片上传失败 %s: %w", local, err)
			return imgTag
		}
		replaced++
		c.logf("  📷 正文图片已上传: %s", filepath.Base(local))
		return strings.Replace(imgTag, src, url, 1)
	})

	if failErr != nil {
		return htmlStr, replaced, failErr
	}
	return result, replaced, nil
}

// imgSrcRe 匹配 <img> 标签的 src 属性（双引号形式，goldmark 输出即为双引号）。
var imgSrcRe = regexp.MustCompile(`(?is)<img[^>]*src="([^"]+)"[^>]*>`)

// AddDraft 接口 4：新增草稿（v2.0 新增 contentSourceURL=文末「阅读原文」链接）。
func (c *Client) AddDraft(title, author, digest, content, thumbMediaID, contentSourceURL string) error {
	if !c.tokenValid() {
		if err := c.refreshToken(); err != nil {
			return err
		}
	}
	// 微信限制：标题最长 64 字（按字符数，Python 版同规则）
	if r := []rune(title); len(r) > 64 {
		title = string(r[:64])
	}
	article := map[string]any{
		"title":                 title,
		"author":                author,
		"digest":                digest,
		"content":               content,
		"thumb_media_id":        thumbMediaID,
		"need_open_comment":     0,
		"only_fans_can_comment": 0,
	}
	if contentSourceURL != "" {
		article["content_source_url"] = contentSourceURL // 文末「阅读原文」跳转
	}
	payload := map[string]any{"articles": []any{article}}
	body, _ := json.Marshal(payload)

	url := fmt.Sprintf("%s/cgi-bin/draft/add?access_token=%s", apiBase, c.token)
	resp, err := c.hc.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("草稿请求失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)

	var out struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
		MediaID string `json:"media_id"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return fmt.Errorf("草稿响应解析失败: %s", string(raw))
	}
	if out.ErrCode != 0 {
		return fmt.Errorf("草稿新增失败: errcode=%d errmsg=%s", out.ErrCode, out.ErrMsg)
	}
	c.logf("  📝 草稿已创建: 《%s》(media_id=%s)", title, out.MediaID)
	return nil
}
