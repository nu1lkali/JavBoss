package emby

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

// fakeEmby 起一个模拟 Emby 服务端，返回 System/Info 和 MediaFolders，
// 并记录每次 Refresh 调用。
type fakeEmby struct {
	server       *httptest.Server
	folders      []mediaFolder
	refreshCalls atomic.Int32
	requireToken string
}

func newFakeEmby(t *testing.T, folders []mediaFolder, requireToken string) *fakeEmby {
	t.Helper()
	f := &fakeEmby{folders: folders, requireToken: requireToken}
	mux := http.NewServeMux()
	mux.HandleFunc("/System/Info", func(w http.ResponseWriter, r *http.Request) {
		f.assertToken(w, r)
		_ = json.NewEncoder(w).Encode(systemInfoResponse{ServerName: "NAS Emby", Version: "4.8.0.50"})
	})
	mux.HandleFunc("/Library/MediaFolders", func(w http.ResponseWriter, r *http.Request) {
		f.assertToken(w, r)
		_ = json.NewEncoder(w).Encode(mediaFoldersResponse{Items: f.folders})
	})
	mux.HandleFunc("/Items/", func(w http.ResponseWriter, r *http.Request) {
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
