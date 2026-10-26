package emby

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestFromConfig(t *testing.T) {
	cfg := map[string]string{
		EnabledKey: "true",
		URLKey:     "http://192.168.1.10:8096/",
		APIKeyKey:  "secret-key",
	}
	s := FromConfig(cfg)
	if !s.Enabled {
		t.Fatal("enabled should be true")
	}
	if s.BaseURL != "http://192.168.1.10:8096" {
		t.Fatalf("base url = %q, want trailing slash trimmed", s.BaseURL)
	}
	if s.APIKey != "secret-key" {
		t.Fatalf("api key = %q", s.APIKey)
	}
	if !s.Configured() {
		t.Fatal("complete settings must be configured")
	}

	partial := FromConfig(map[string]string{EnabledKey: "1", URLKey: "http://nas:8096"})
	if partial.Configured() {
		t.Fatal("missing api key must not be configured")
	}
	if disabled := FromConfig(map[string]string{EnabledKey: "false", URLKey: "http://nas:8096", APIKeyKey: "k"}); disabled.Configured() {
		t.Fatal("disabled settings must not be configured")
	}
}

func TestPublicConfigRedactsAPIKey(t *testing.T) {
	cfg := map[string]string{
		EnabledKey: "true",
		URLKey:     "http://nas:8096",
		APIKeyKey:  "super-secret",
	}
	PublicConfig(cfg)
	if _, ok := cfg[APIKeyKey]; ok {
		t.Fatal("api key must be removed from public config")
	}
	if cfg[APIKeySetKey] != "true" {
		t.Fatalf("api_key_set = %q, want true", cfg[APIKeySetKey])
	}
	if cfg[URLKey] != "http://nas:8096" {
		t.Fatalf("url must survive: %q", cfg[URLKey])
	}

	empty := map[string]string{EnabledKey: "true", URLKey: "http://nas:8096"}
	PublicConfig(empty)
	if empty[APIKeySetKey] != "false" {
		t.Fatalf("api_key_set = %q, want false", empty[APIKeySetKey])
	}
}

// fakeEmby 起一个模拟 Emby 服务端，返回 System/Info、System/Info/Public 和
// MediaFolders，并记录每次 Refresh 调用与收到的请求路径顺序。
type fakeEmby struct {
	server       *httptest.Server
	folders      []mediaFolder
	refreshCalls atomic.Int32
	requireToken string

	mu        sync.Mutex
	requests  []string
	public404 bool
}

func newFakeEmby(t *testing.T, folders []mediaFolder, requireToken string) *fakeEmby {
	t.Helper()
	f := &fakeEmby{folders: folders, requireToken: requireToken}
	mux := http.NewServeMux()
	mux.HandleFunc("/System/Info/Public", func(w http.ResponseWriter, r *http.Request) {
		f.recordPath(r.URL.Path)
		if f.public404 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("X-Emby-Token") != "" {
			// Public 端是免认证端点，带 token 说明调用方用错了地方。
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(systemInfoResponse{ServerName: "NAS Emby", Version: "4.8.0.50"})
	})
	mux.HandleFunc("/System/Info", func(w http.ResponseWriter, r *http.Request) {
		f.recordPath(r.URL.Path)
		f.assertToken(w, r)
		_ = json.NewEncoder(w).Encode(systemInfoResponse{ServerName: "NAS Emby", Version: "4.8.0.50"})
	})
	mux.HandleFunc("/Library/MediaFolders", func(w http.ResponseWriter, r *http.Request) {
		f.recordPath(r.URL.Path)
		f.assertToken(w, r)
		_ = json.NewEncoder(w).Encode(mediaFoldersResponse{Items: f.folders})
	})
	mux.HandleFunc("/Items/", func(w http.ResponseWriter, r *http.Request) {
		f.recordPath(r.URL.Path)
		f.assertToken(w, r)
		if !strings.Contains(r.URL.Path, "/Refresh") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		f.refreshCalls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeEmby) assertToken(w http.ResponseWriter, r *http.Request) {
	if f.requireToken != "" && r.Header.Get("X-Emby-Token") != f.requireToken {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
}

// recordPath 按顺序记录收到的请求路径（handler goroutine 与测试协程都会访问）。
func (f *fakeEmby) recordPath(path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, path)
}

// requestPaths 返回按顺序记录的请求路径。
func (f *fakeEmby) requestPaths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

func TestTestConnectionSuccess(t *testing.T) {
	fake := newFakeEmby(t, nil, "good-key")
	info, err := TestConnection(context.Background(), fake.server.URL, "good-key")
	if err != nil {
		t.Fatalf("test connection: %v", err)
	}
	if info.ServerName != "NAS Emby" {
		t.Fatalf("server name = %q", info.ServerName)
	}
	if info.Version != "4.8.0.50" {
		t.Fatalf("version = %q", info.Version)
	}
}

// TestTestConnectionParsesLargeSystemInfo 复现真实故障：真实 Emby 的 /System/Info
// 带 WakeOnLanInfo 等长列表，整个响应远超 maxErrorBody*4（800 字节）。读取路径曾用
// 这个小限制读 body，JSON 被物理截断后 Unmarshal 报 unexpected end of JSON input，
// 被误判成"可能不是 Emby 服务"，而 Content-Type 明明是 application/json。
func TestTestConnectionParsesLargeSystemInfo(t *testing.T) {
	large := `{"SystemUpdateLevel":"Release","OperatingSystemDisplayName":"Unix","WakeOnLanInfo":[` +
		strings.Repeat(`{"MacAddress":"00E04C68E70F","BroadcastAddress":"192.168.2.255","Port":9},`, 20) +
		`{"MacAddress":"","BroadcastAddress":"192.168.2.255","Port":9}],"ServerName":"nu1lkali","Version":"4.10.1.0","Id":"55d1d9a5b63e4549aa9fbbe74a76db8c"}`
	if len(large) <= maxErrorBody*4 {
		t.Fatalf("test body must exceed %d bytes to exercise truncation, got %d", maxErrorBody*4, len(large))
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = io.WriteString(w, large)
	}))
	defer srv.Close()

	info, err := TestConnection(context.Background(), srv.URL, "key")
	if err != nil {
		t.Fatalf("test connection: %v", err)
	}
	if info.ServerName != "nu1lkali" {
		t.Fatalf("server name = %q", info.ServerName)
	}
	if info.Version != "4.10.1.0" {
		t.Fatalf("version = %q", info.Version)
	}
}

// TestTestConnectionProbesPublicEndpointFirst 验证连接测试先用免认证的
// /System/Info/Public 确认服务器身份，且这一跳不带 X-Emby-Token
// （fake 对带 token 的 Public 请求回 400，顺序错了或带了 token 就连不上）。
func TestTestConnectionProbesPublicEndpointFirst(t *testing.T) {
	fake := newFakeEmby(t, nil, "good-key")
	info, err := TestConnection(context.Background(), fake.server.URL, "good-key")
	if err != nil {
		t.Fatalf("test connection: %v", err)
	}
	if info.ServerName != "NAS Emby" {
		t.Fatalf("server name = %q", info.ServerName)
	}
	paths := fake.requestPaths()
	if len(paths) != 2 || paths[0] != "/System/Info/Public" || paths[1] != "/System/Info" {
		t.Fatalf("request order = %v, want [/System/Info/Public /System/Info]", paths)
	}
}

// TestTestConnectionBlamesKeyWhenPublicSucceeds：Public 端确认了服务器身份后，
// 带 Key 的请求失败必须明确归因到 API Key，而不是让用户怀疑地址。
func TestTestConnectionBlamesKeyWhenPublicSucceeds(t *testing.T) {
	fake := newFakeEmby(t, nil, "good-key")
	_, err := TestConnection(context.Background(), fake.server.URL, "wrong-key")
	if err == nil {
		t.Fatal("expected auth failure")
	}
	msg := err.Error()
	if !strings.Contains(msg, "是 Emby 服务器") || !strings.Contains(msg, "API Key") {
		t.Fatalf("error should confirm server identity and blame the key: %v", err)
	}
	if strings.Contains(msg, "good-key") || strings.Contains(msg, "wrong-key") {
		t.Fatalf("error must not leak the key: %v", err)
	}
}

// TestTestConnectionSucceedsWhenPublicEndpointMissing：个别部署（反代规则）
// 可能挡掉 Public 端点，此时不能误判，继续用带 Key 的完整接口完成测试。
func TestTestConnectionSucceedsWhenPublicEndpointMissing(t *testing.T) {
	fake := newFakeEmby(t, nil, "good-key")
	fake.public404 = true
	info, err := TestConnection(context.Background(), fake.server.URL, "good-key")
	if err != nil {
		t.Fatalf("test connection: %v", err)
	}
	if info.ServerName != "NAS Emby" {
		t.Fatalf("server name = %q", info.ServerName)
	}
}

func TestTestConnectionRejectsBadKey(t *testing.T) {
	fake := newFakeEmby(t, nil, "good-key")
	_, err := TestConnection(context.Background(), fake.server.URL, "wrong-key")
	if err == nil {
		t.Fatal("expected auth failure")
	}
	if !strings.Contains(err.Error(), "API Key") {
		t.Fatalf("error should mention API key: %v", err)
	}
}

// Emby 的认证失败可能不走 401：AuthService 抛 SecurityException 后响应是
// 2xx + 纯文本 "Access token is invalid or expired."，曾被我方误报成
// "可能不是 Emby 服务"。这里锁定按响应体文案识别为 API Key 问题。
func TestTestConnectionReportsExpiredTokenAsAuthFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Access token is invalid or expired."))
	}))
	t.Cleanup(server.Close)
	_, err := TestConnection(context.Background(), server.URL, "stale-key")
	if err == nil {
		t.Fatal("expected auth failure")
	}
	if !strings.Contains(err.Error(), "API Key") {
		t.Fatalf("expired token should be reported as API key failure: %v", err)
	}
}

// 2xx 但返回网页：端口被其他服务/反代登录页占用时的典型表现，
// 错误信息必须带上状态码和 Content-Type，便于用户识别应答者。
func TestTestConnectionReportsHTMLResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<!DOCTYPE html><html><head><title>管理页</title></head><body>login</body></html>"))
	}))
	t.Cleanup(server.Close)
	_, err := TestConnection(context.Background(), server.URL, "key")
	if err == nil {
		t.Fatal("expected error for non-JSON response")
	}
	msg := err.Error()
	for _, want := range []string{"200", "网页", "text/html"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error should mention %q: %v", want, err)
		}
	}
}

// 2xx 但响应体为空：同样要给出可操作的提示而不是裸的 JSON 解析错误。
func TestTestConnectionReportsEmptyResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	_, err := TestConnection(context.Background(), server.URL, "key")
	if err == nil {
		t.Fatal("expected error for empty response")
	}
	if !strings.Contains(err.Error(), "空响应") {
		t.Fatalf("error should mention empty response: %v", err)
	}
}

func TestTestConnectionRejectsUnreachableAndInvalidURL(t *testing.T) {
	if _, err := TestConnection(context.Background(), "http://127.0.0.1:1", "key"); err == nil {
		t.Fatal("expected unreachable error")
	}
	for _, bad := range []string{"", "nas.local:8096", "ftp://nas", "/volume1/video"} {
		if _, err := TestConnection(context.Background(), bad, "key"); err == nil {
			t.Fatalf("expected rejection for %q", bad)
		}
	}
}

func TestRefreshLibrariesPrefersMovieLibraries(t *testing.T) {
	fake := newFakeEmby(t, []mediaFolder{
		{ID: "1", Name: "电影", CollectionType: "movies"},
		{ID: "2", Name: "音乐", CollectionType: "music"},
	}, "key")
	refreshed, err := RefreshLibraries(context.Background(), Settings{
		Enabled: true, BaseURL: fake.server.URL, APIKey: "key",
	})
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if refreshed != 1 {
		t.Fatalf("refreshed = %d, want only the movie library", refreshed)
	}
	if got := fake.refreshCalls.Load(); got != 1 {
		t.Fatalf("refresh calls = %d, want 1", got)
	}
}

func TestRefreshLibrariesFallsBackToAllLibraries(t *testing.T) {
	fake := newFakeEmby(t, []mediaFolder{
		{ID: "1", Name: "默认库", CollectionType: ""},
		{ID: "2", Name: "电影2", CollectionType: "movies"},
	}, "key")
	refreshed, err := RefreshLibraries(context.Background(), Settings{
		Enabled: true, BaseURL: fake.server.URL, APIKey: "key",
	})
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	// 有 movies 库时只刷 movies：这里 movies 有一个，刷新 1 个。
	if refreshed != 1 {
		t.Fatalf("refreshed = %d, want 1", refreshed)
	}
}

func TestRefreshLibrariesWithoutMoviesRefreshesEverything(t *testing.T) {
	fake := newFakeEmby(t, []mediaFolder{
		{ID: "1", Name: "家庭视频", CollectionType: "homevideos"},
		{ID: "2", Name: "音乐", CollectionType: "music"},
	}, "key")
	refreshed, err := RefreshLibraries(context.Background(), Settings{
		Enabled: true, BaseURL: fake.server.URL, APIKey: "key",
	})
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if refreshed != 2 {
		t.Fatalf("refreshed = %d, want 2 (fallback to all)", refreshed)
	}
}

func TestRefreshLibrariesSkipsWhenNotConfigured(t *testing.T) {
	fake := newFakeEmby(t, []mediaFolder{{ID: "1", CollectionType: "movies"}}, "")
	refreshed, err := RefreshLibraries(context.Background(), Settings{BaseURL: fake.server.URL})
	if err != nil || refreshed != 0 {
		t.Fatalf("incomplete settings must be a no-op: refreshed=%d err=%v", refreshed, err)
	}
	if got := fake.refreshCalls.Load(); got != 0 {
		t.Fatalf("refresh calls = %d, want 0", got)
	}
}

func resetNotifyState(t *testing.T) {
	t.Helper()
	notifyMu.Lock()
	lastNotifyTime = time.Time{}
	notifyMu.Unlock()
	t.Cleanup(func() {
		notifyMu.Lock()
		lastNotifyTime = time.Time{}
		notifyMu.Unlock()
	})
}

func TestNotifyRefreshDisabledIsSilentNoOp(t *testing.T) {
	resetNotifyState(t)
	fake := newFakeEmby(t, []mediaFolder{{ID: "1", CollectionType: "movies"}}, "key")
	cfg := map[string]string{
		EnabledKey: "false",
		URLKey:     fake.server.URL,
		APIKeyKey:  "key",
	}
	NotifyRefresh(context.Background(), cfg, true)
	if got := fake.refreshCalls.Load(); got != 0 {
		t.Fatalf("refresh calls = %d, want 0 when disabled", got)
	}
}

func TestNotifyRefreshForceTriggersImmediately(t *testing.T) {
	resetNotifyState(t)
	fake := newFakeEmby(t, []mediaFolder{{ID: "1", CollectionType: "movies"}}, "key")
	cfg := map[string]string{
		EnabledKey: "true",
		URLKey:     fake.server.URL,
		APIKeyKey:  "key",
	}
	NotifyRefresh(context.Background(), cfg, true)
	if got := fake.refreshCalls.Load(); got != 1 {
		t.Fatalf("refresh calls = %d, want 1 with force", got)
	}
}

func TestNotifyRefreshDebouncesWithoutForce(t *testing.T) {
	resetNotifyState(t)
	fake := newFakeEmby(t, []mediaFolder{{ID: "1", CollectionType: "movies"}}, "key")
	cfg := map[string]string{
		EnabledKey: "true",
		URLKey:     fake.server.URL,
		APIKeyKey:  "key",
	}
	NotifyRefresh(context.Background(), cfg, false)
	if got := fake.refreshCalls.Load(); got != 1 {
		t.Fatalf("first notify should trigger: calls = %d", got)
	}
	// 间隔内再次触发应被跳过；force=true 才放行。
	NotifyRefresh(context.Background(), cfg, false)
	if got := fake.refreshCalls.Load(); got != 1 {
		t.Fatalf("second notify within interval should be skipped: calls = %d", got)
	}
	NotifyRefresh(context.Background(), cfg, true)
	if got := fake.refreshCalls.Load(); got != 2 {
		t.Fatalf("forced notify should bypass debounce: calls = %d", got)
	}
}

func TestNotifyRefreshFailureDoesNotPanic(t *testing.T) {
	// 错误路径只记日志，不影响调用方；这里验证失败时刷新次数为 0 且不 panic。
	resetNotifyState(t)
	fake := newFakeEmby(t, []mediaFolder{{ID: "1", CollectionType: "movies"}}, "expected-key")
	// 用错误的 key 让请求 401。
	cfg := map[string]string{
		EnabledKey: "true",
		URLKey:     fake.server.URL,
		APIKeyKey:  "wrong",
	}
	NotifyRefresh(context.Background(), cfg, true)
	if got := fake.refreshCalls.Load(); got != 0 {
		t.Fatalf("auth failure must not refresh: calls = %d", got)
	}
}
