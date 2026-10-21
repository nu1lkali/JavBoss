// Package emby 负责在 NFO 写入完成后通知 Emby 刷新媒体库。
//
// 背景：Emby 界面永远读它自己的 library.db，磁盘上的 NFO 只在"扫描/刷新元数据"时
// 才会被重新解析。JavBoss 批量重建 NFO 后，如果不通知 Emby，用户看到的元数据
// 仍然是旧数据，必须手动去 Emby 点一次刷新。这个包把最后这一步自动化。
//
// 密钥处理：API Key 存在配置表里，对外接口一律不返回明文（只返回"是否已配置"），
// 日志里也不打印密钥。
package emby

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"javboss/internal/common/logging"
)

// 配置表键名。开关与地址存在配置表里，改即时生效，不需要重启容器。
const (
	EnabledKey   = "jav_emby_enabled"
	URLKey       = "jav_emby_url"
	APIKeyKey    = "jav_emby_api_key"
	APIKeySetKey = "jav_emby_api_key_set"
)

// requestTimeout 单次 Emby 请求的超时。刷新整库可能较久，但每个库一个请求，
// 单个请求本身应该很快返回。
const requestTimeout = 15 * time.Second

// notifyMinInterval 是两次自动刷新之间的最小间隔。
// 自动整理每完成一个目录都可能触发刷新，没有这个间隔会频繁打扰 Emby。
// 批量重建是用户手动操作，用 force 绕过这个限制。
const notifyMinInterval = 10 * time.Minute

// maxErrorBody 限制错误响应体进入日志/返回给前端的长度，避免把整个 HTML 错误页塞进去。
const maxErrorBody = 200

// Settings 是通知 Emby 所需的配置。
type Settings struct {
	Enabled bool
	BaseURL string
	APIKey  string
}

// Configured 判断配置是否完整到可以发起请求。
func (s Settings) Configured() bool {
	return s.Enabled && s.BaseURL != "" && s.APIKey != ""
}

// FromConfig 从配置表读取 Emby 联动设置。
func FromConfig(cfg map[string]string) Settings {
	s := Settings{
		BaseURL: NormalizeBaseURL(cfg[URLKey]),
		APIKey:  strings.TrimSpace(cfg[APIKeyKey]),
	}
	if cfg == nil {
		return s
	}
	s.Enabled = parseBool(cfg[EnabledKey])
	return s
}

// PublicConfig 在配置对外返回前调用：删除密钥明文，只保留"是否已配置"的布尔值。
// 与翻译配置（jav_title_translation_profiles）的脱敏方式保持一致。
func PublicConfig(cfg map[string]string) {
	if cfg == nil {
		return
	}
	cfg[APIKeySetKey] = strconv.FormatBool(strings.TrimSpace(cfg[APIKeyKey]) != "")
	delete(cfg, APIKeyKey)
}

// ServerInfo 是测试连接的返回，展示给用户确认连的是哪台服务器。
type ServerInfo struct {
	ServerName string `json:"server_name"`
	Version    string `json:"version"`
}

type systemInfoResponse struct {
	ServerName string `json:"ServerName"`
	Version    string `json:"Version"`
}

type mediaFoldersResponse struct {
	Items []mediaFolder `json:"Items"`
}

type mediaFolder struct {
	ID             string `json:"Id"`
	Name           string `json:"Name"`
	CollectionType string `json:"CollectionType"`
}

// TestConnection 用给定的地址和密钥连接 Emby。
// 地址和密钥允许是"刚填了还没保存"的值，所以作为参数传入而不是读配置。
func TestConnection(ctx context.Context, baseURL, apiKey string) (ServerInfo, error) {
	base := NormalizeBaseURL(baseURL)
	key := strings.TrimSpace(apiKey)
	if base == "" {
		return ServerInfo{}, errors.New("请先填写 Emby 地址")
	}
	if key == "" {
		return ServerInfo{}, errors.New("请先填写 API Key")
	}
	if err := ValidateBaseURL(base); err != nil {
		return ServerInfo{}, err
	}
	var info systemInfoResponse
	if err := getJSON(ctx, base+"/System/Info", key, &info); err != nil {
		return ServerInfo{}, err
	}
	result := ServerInfo{
		ServerName: strings.TrimSpace(info.ServerName),
		Version:    strings.TrimSpace(info.Version),
	}
	if result.ServerName == "" {
		result.ServerName = base
	}
	return result, nil
}

// RefreshLibraries 让 Emby 重新扫描所有（电影类）媒体库的元数据。
// 返回实际刷新的库数量。
func RefreshLibraries(ctx context.Context, s Settings) (int, error) {
	if !s.Configured() {
		return 0, nil
	}
	if err := ValidateBaseURL(s.BaseURL); err != nil {
		return 0, err
	}
	var folders mediaFoldersResponse
	if err := getJSON(ctx, s.BaseURL+"/Library/MediaFolders", s.APIKey, &folders); err != nil {
		return 0, err
	}
	targets := selectFolders(folders.Items)
	refreshed := 0
	var firstErr error
	for _, folder := range targets {
		if err := refreshFolder(ctx, s, folder.ID); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("刷新媒体库 %q 失败：%v", folder.Name, err)
			}
			continue
		}
		refreshed++
	}
	return refreshed, firstErr
}

// selectFolders 优先刷新电影库；一个电影库都没有时退回刷新全部库，
// 避免用户的库类型设置不同就完全刷不到。
func selectFolders(items []mediaFolder) []mediaFolder {
	movies := make([]mediaFolder, 0, len(items))
	for _, item := range items {
		if item.ID == "" {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(item.CollectionType), "movies") {
			movies = append(movies, item)
		}
	}
	if len(movies) > 0 {
		return movies
	}
	all := make([]mediaFolder, 0, len(items))
	for _, item := range items {
		if item.ID != "" {
			all = append(all, item)
		}
	}
	return all
}

// refreshFolder 对单个媒体库发起刷新。ReplaceAllMetadata/ReplaceAllImages
// 必须带上，否则 Emby 默认保留条目上已有的元数据，NFO 改了也不会生效。
func refreshFolder(ctx context.Context, s Settings, folderID string) error {
	url := fmt.Sprintf(
		"%s/Items/%s/Refresh?Recursive=true&MetadataRefreshMode=FullRefresh&ImageRefreshMode=FullRefresh&ReplaceAllMetadata=true&ReplaceAllImages=true",
		s.BaseURL, folderID,
	)
	return postNoContent(ctx, url, s.APIKey)
}

var (
	notifyMu       sync.Mutex
	lastNotifyTime time.Time
)

// NotifyRefresh 在 NFO 写入完成后通知 Emby 刷新。
//
// force=true 用于用户手动触发的批量重建（立即刷新）；
// force=false 用于自动整理（受 notifyMinInterval 限制，避免频繁打扰 Emby）。
// 未配置、配置不完整时静默返回；失败只记日志，绝不影响主流程。
func NotifyRefresh(ctx context.Context, cfg map[string]string, force bool) {
	if cfg == nil {
		return
	}
	s := FromConfig(cfg)
	if !s.Configured() {
		return
	}
	notifyMu.Lock()
	if !force && !lastNotifyTime.IsZero() && time.Since(lastNotifyTime) < notifyMinInterval {
		notifyMu.Unlock()
		return
	}
	notifyMu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, requestTimeout*time.Duration(2))
	defer cancel()

	refreshed, err := RefreshLibraries(ctx, s)
	notifyMu.Lock()
	if err == nil {
		lastNotifyTime = time.Now()
	}
	notifyMu.Unlock()

	if err != nil {
		logging.Error("emby refresh failed url=%s libraries=%d err=%v", s.BaseURL, refreshed, err)
		return
	}
	if refreshed > 0 {
		logging.Info("emby refresh triggered url=%s libraries=%d", s.BaseURL, refreshed)
	}
}

func getJSON(ctx context.Context, url, apiKey string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("请求地址无效：%v", err)
	}
	req.Header.Set("Accept", "application/json")
	if apiKey != "" {
		req.Header.Set("X-Emby-Token", apiKey)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return friendlyRequestError(url, err)
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody*4))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return statusError(resp.StatusCode, string(body))
	}
	if readErr != nil {
		return fmt.Errorf("读取响应失败：%v", readErr)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("响应不是有效的 JSON（%s 可能不是 Emby 服务）", url)
	}
	return nil
}

func postNoContent(ctx context.Context, url, apiKey string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return fmt.Errorf("请求地址无效：%v", err)
	}
	if apiKey != "" {
		req.Header.Set("X-Emby-Token", apiKey)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return friendlyRequestError(url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody*4))
		return statusError(resp.StatusCode, string(body))
	}
	return nil
}

var httpClient = &http.Client{Timeout: requestTimeout}

// friendlyRequestError 把底层网络错误翻译成用户能行动的中文提示。
// 注意：绝不把 API Key 放进错误信息（Key 不在 URL 里，但防御性地不拼 query）。
func friendlyRequestError(url string, err error) error {
	var netErr interface{ Timeout() bool }
	if errors.As(err, &netErr) && netErr.Timeout() {
		return fmt.Errorf("连接 %s 超时，请检查地址和网络", url)
	}
	return fmt.Errorf("无法连接 %s：%v", url, err)
}

func statusError(code int, body string) error {
	switch {
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		return errors.New("认证失败，请检查 API Key")
	case code == http.StatusNotFound:
		return errors.New("接口不存在，请确认地址是 Emby 服务器（如 http://192.168.1.10:8096）")
	default:
		detail := strings.TrimSpace(body)
		if len([]rune(detail)) > maxErrorBody {
			detail = string([]rune(detail)[:maxErrorBody]) + "…"
		}
		if detail == "" {
			return fmt.Errorf("Emby 返回 HTTP %d", code)
		}
		return fmt.Errorf("Emby 返回 HTTP %d：%s", code, detail)
	}
}

// NormalizeBaseURL 去掉首尾空白和尾部斜杠，保证拼接路径时不会出现 "//"。
func NormalizeBaseURL(raw string) string {
	value := strings.TrimSpace(raw)
	value = strings.TrimRight(value, "/")
	return value
}

// ValidateBaseURL 只允许 http/https，防止把奇怪的内容（比如文件路径）当成地址。
func ValidateBaseURL(base string) error {
	lower := strings.ToLower(base)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return errors.New("地址必须以 http:// 或 https:// 开头")
	}
	if strings.ContainsAny(base, " \t\r\n") {
		return errors.New("地址包含无效字符")
	}
	return nil
}

func parseBool(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
