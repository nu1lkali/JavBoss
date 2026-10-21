package common

import (
	"os"
	"strconv"
	"strings"
	"time"
)

func envBool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// ContainerMode reports whether JavBoss is running in a container-oriented mode.
func ContainerMode() bool {
	return envBool("JAVBOSS_CONTAINER")
}

func HostPathPrefixEnabled() bool {
	return envBool("JAVBOSS_HOST_PATH_PREFIX")
}

func ProxyHostGatewayEnabled() bool {
	return envBool("JAVBOSS_PROXY_HOST_GATEWAY")
}

// AutoProcessMode 返回扫描后自动整理的目录处理模式，未开启时返回空字符串。
//
//	JAVBOSS_AUTO_PROCESS=sidecar                 只写 NFO 与封面（推荐，不动文件）
//	JAVBOSS_AUTO_PROCESS=organize                只按番号整理移动
//	JAVBOSS_AUTO_PROCESS=organize_with_sidecar   整理并写 NFO 与封面
//
// 只有白名单里的模式才算开启：拼错的取值直接视为关闭，
// 避免设置页因为“看起来开着、实际没生效”而误导用户。
func AutoProcessMode() string {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv("JAVBOSS_AUTO_PROCESS")))
	switch raw {
	case "", "0", "false", "no", "off", "none":
		return ""
	case "1", "true", "yes", "on":
		// 布尔写法等同于“只写 NFO 与封面”，这是最不容易误伤文件的行为。
		return "sidecar"
	}
	switch raw {
	case "sidecar", "organize", "organize_with_sidecar":
		return raw
	default:
		return ""
	}
}

// AutoProcessCoverWait 返回自动整理前等待封面队列排空的上限。
// 支持 Go duration 字符串（如 "10m"、"600s"），也接受纯秒数。
// 返回 0 表示使用内置默认值；返回负值表示不等待封面，直接用现有缓存整理。
func AutoProcessCoverWait() time.Duration {
	raw := strings.TrimSpace(os.Getenv("JAVBOSS_AUTO_PROCESS_COVER_WAIT"))
	if raw == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(raw); err == nil {
		if seconds < 0 {
			return -1
		}
		return time.Duration(seconds) * time.Second
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return 0
	}
	if parsed < 0 {
		return -1
	}
	return parsed
}
