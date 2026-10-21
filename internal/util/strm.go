package util

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf8"
)

// StrmExtension 是 Emby/Jellyfin 风格的流媒体指针文件扩展名。
const StrmExtension = ".strm"

// strmMaxBytes 限制单个 STRM 文件的读取长度，避免误读超大文本文件。
const strmMaxBytes = 64 << 10

// StrmTargetKind 描述 STRM 指针指向的目标类型。
type StrmTargetKind int

const (
	// StrmTargetUnknown 表示文件为空或没有可用的目标行。
	StrmTargetUnknown StrmTargetKind = iota
	// StrmTargetURL 表示 http/https 等网络流地址。
	StrmTargetURL
	// StrmTargetPath 表示本地文件系统路径（绝对路径或 Windows UNC 路径）。
	StrmTargetPath
)

// StrmReference 是 STRM 文件解析后的目标信息。
type StrmReference struct {
	// Raw 是去掉 BOM 和首尾空白后的原始目标行。
	Raw string
	// Kind 是目标类型。
	Kind StrmTargetKind
	// URL 在 Kind 为 StrmTargetURL 时保存完整地址。
	URL string
	// Path 在 Kind 为 StrmTargetPath 时保存解析后的本地路径。
	Path string
	// Extension 是目标媒体文件的扩展名（小写，含点），未知时为空。
	Extension string
}

// IsStrmFile 判断路径是否是以 .strm 结尾的指针文件。
func IsStrmFile(path string) bool {
	return strings.EqualFold(filepath.Ext(path), StrmExtension)
}

// ReadStrmReference 解析 STRM 文件，返回其中第一条有效目标行。
// 读取失败或没有有效目标时返回错误，调用方可据此降级为按文件名刮削。
func ReadStrmReference(path string) (*StrmReference, error) {
	data, err := readStrmContent(path)
	if err != nil {
		return nil, err
	}
	return ParseStrmReferenceBytes(data)
}

func readStrmContent(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	buffer := make([]byte, strmMaxBytes)
	n, err := file.Read(buffer)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return buffer[:n], nil
}

// ParseStrmReference 解析 STRM 文本内容。
// 规则与 Emby/Jellyfin 保持一致：跳过空行和 # 注释行，取第一条有效行，
// 兼容带 BOM 的 UTF-8/UTF-16(LE/BE) 文本。
func ParseStrmReference(content string) (*StrmReference, error) {
	return ParseStrmReferenceBytes([]byte(content))
}

// ParseStrmReferenceBytes 与 ParseStrmReference 相同，但以字节形式接收内容，
// 以便正确识别 UTF-16 编码（字符串转换会破坏多字节序列）。
func ParseStrmReferenceBytes(content []byte) (*StrmReference, error) {
	text := decodeStrmText(content)
	scanner := bufio.NewScanner(strings.NewReader(text))
	scanner.Buffer(make([]byte, 0, 1024), strmMaxBytes)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		return newStrmReference(line), nil
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read strm content: %w", err)
	}
	return nil, errors.New("strm file has no target")
}

func newStrmReference(line string) *StrmReference {
	ref := &StrmReference{Raw: line}
	scheme := strings.ToLower(uriScheme(line))
	switch scheme {
	case "http", "https", "rtsp", "rtmp", "rtmps", "mms", "udp", "rtp", "ftp", "smb":
		ref.Kind = StrmTargetURL
		ref.URL = line
		ref.Extension = extensionFromTarget(line)
		return ref
	}

	// 其余情况按本地路径处理，Windows 的 F:/...、F:\... 与 \\NAS\... 都归入此分支。
	ref.Kind = StrmTargetPath
	ref.Path = normalizeStrmLocalPath(line)
	ref.Extension = extensionFromTarget(line)
	return ref
}

// normalizeStrmLocalPath 把指针中的本地路径转换为当前平台的路径形式。
// 指针文件常由 Windows 环境生成，因此反斜杠路径需要单独处理：
// filepath.FromSlash 只认 Windows 分隔符之外的写法，直接在 Unix 上使用会保留反斜杠。
func normalizeStrmLocalPath(target string) string {
	trimmed := strings.TrimSpace(target)
	if runtime.GOOS == "windows" {
		return filepath.FromSlash(trimmed)
	}
	return strings.ReplaceAll(trimmed, `\`, "/")
}

// uriScheme 返回小写的 scheme（不含冒号）；不是 URI 时返回空串。
func uriScheme(raw string) string {
	idx := strings.Index(raw, "://")
	if idx <= 0 {
		return ""
	}
	scheme := raw[:idx]
	for _, r := range scheme {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '+' || r == '-' || r == '.' {
			continue
		}
		return ""
	}
	return strings.ToLower(scheme)
}

// extensionFromTarget 从 URL 或路径中提取媒体扩展名。
// 会忽略查询串和片段，以保证 ".../ABP-123.mp4?token=x" 仍能识别出 .mp4。
func extensionFromTarget(target string) string {
	trimmed := target
	if idx := strings.IndexAny(trimmed, "?#"); idx >= 0 {
		trimmed = trimmed[:idx]
	}
	trimmed = strings.TrimRight(trimmed, "/")
	return strings.ToLower(filepath.Ext(trimmed))
}

// StrmPlaybackTarget 是 STRM 条目实际可播放的目标。
type StrmPlaybackTarget struct {
	// URL 在网络流目标时非空。
	URL string
	// LocalPath 在本地文件目标时非空。
	LocalPath string
	// Extension 是目标媒体扩展名（小写，含点）。
	Extension string
}

// IsURL 报告目标是否为网络流地址。
func (t StrmPlaybackTarget) IsURL() bool { return t.URL != "" }

// IsLocal 报告目标是否为本地文件路径。
func (t StrmPlaybackTarget) IsLocal() bool { return t.LocalPath != "" }

// StrmPlaybackTargetFromReference 从解析结果得到可播放目标。
// 无法解析的目标返回 ok=false。
func StrmPlaybackTargetFromReference(ref *StrmReference) (StrmPlaybackTarget, bool) {
	if ref == nil {
		return StrmPlaybackTarget{}, false
	}
	switch ref.Kind {
	case StrmTargetURL:
		if strings.TrimSpace(ref.URL) == "" {
			return StrmPlaybackTarget{}, false
		}
		return StrmPlaybackTarget{URL: ref.URL, Extension: ref.Extension}, true
	case StrmTargetPath:
		if strings.TrimSpace(ref.Path) == "" {
			return StrmPlaybackTarget{}, false
		}
		return StrmPlaybackTarget{LocalPath: ref.Path, Extension: ref.Extension}, true
	default:
		return StrmPlaybackTarget{}, false
	}
}

// StrmFingerprint 为 STRM 文件生成稳定的去重指纹。
//
// 普通视频依靠容器元数据 + 文件大小识别同一份内容，而 STRM 只是指针文件，
// 因此指纹由“目录内相对路径 + 指针目标”组成：
//   - 目标可解析时，同一目录下指向同一地址的两份 STRM 视为同一个视频；
//   - 目标不可解析（空文件/读取失败）时退化为按相对路径区分。
//
// ref 为 nil 表示解析失败。
func StrmFingerprint(directoryID int64, relativePath string, ref *StrmReference) string {
	pathKey := filepath.ToSlash(strings.TrimSpace(relativePath))
	if directoryID > 0 {
		pathKey = fmt.Sprintf("%d:%s", directoryID, pathKey)
	}
	if ref == nil || strings.TrimSpace(ref.Raw) == "" {
		return "strm:path:" + pathKey
	}
	return "strm:target:" + strings.ToLower(strings.TrimSpace(ref.Raw))
}

// decodeStrmText 去除 BOM 并转换常见 UTF-16 文本，输出 UTF-8 字符串。
func decodeStrmText(raw []byte) string {
	switch {
	case bytes.HasPrefix(raw, []byte{0xEF, 0xBB, 0xBF}):
		return string(raw[3:])
	case bytes.HasPrefix(raw, []byte{0xFF, 0xFE}):
		return decodeUTF16(raw[2:], false)
	case bytes.HasPrefix(raw, []byte{0xFE, 0xFF}):
		return decodeUTF16(raw[2:], true)
	}
	if utf8.Valid(raw) {
		return strings.TrimPrefix(string(raw), "\uFEFF")
	}
	// 非 UTF-8 且无 BOM 时尝试按 UTF-16LE 处理（Windows 记事本的常见输出）。
	return decodeUTF16(raw, false)
}

func decodeUTF16(raw []byte, bigEndian bool) string {
	if len(raw)%2 == 1 {
		raw = raw[:len(raw)-1]
	}
	builder := strings.Builder{}
	builder.Grow(len(raw) / 2)
	for i := 0; i+1 < len(raw); i += 2 {
		var unit uint16
		if bigEndian {
			unit = uint16(raw[i])<<8 | uint16(raw[i+1])
		} else {
			unit = uint16(raw[i+1])<<8 | uint16(raw[i])
		}
		builder.WriteRune(rune(unit))
	}
	return builder.String()
}
