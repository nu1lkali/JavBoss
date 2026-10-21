package util

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// Accept patterns like "ipx-633", "ipx633", "ipx633_ch", "ipx-714c" (letter suffix ignored).
var CodeRe = regexp.MustCompile(`(?i)([a-z]{2,6})[-_ ]?(\d{2,5})([a-z]{0,2})`)

var (
	fc2PPVCodeRe                = regexp.MustCompile(`(?i)(^|[^a-z0-9])fc2[-_ ]*ppv[-_ ]*([0-9]+)`)
	alphaNumericUncensoredRe    = regexp.MustCompile(`(?i)(^|[^a-z0-9])([a-z]+)(?:\s*([-_ ])\s*)?(\d{2,})([^a-z0-9]|$)`)
	mixedPrefixUncensoredRe     = regexp.MustCompile(`(?i)(^|[^a-z0-9])([a-z0-9]*[a-z][a-z0-9]*\d[a-z0-9]*[a-z][a-z0-9]*)[-_ ](\d{2,})([^a-z0-9]|$)`)
	mixedPrefixCensoredRe       = regexp.MustCompile(`(?i)(^|[^a-z0-9])([a-z][a-z0-9]{1,5})[-_ ](\d{2,5})([a-z]{0,2})([^a-z0-9]|$)`)
	pureNumericUncensoredCodeRe = regexp.MustCompile(`(^|[^0-9])(\d{4,}[-_]\d{2,})([^0-9]|$)`)
	explicitShortCodeRe         = regexp.MustCompile(`(?i)(^|[^a-z0-9])([a-z]{2,6})[-_ ](\d{2})([a-z]{0,2})([^a-z0-9]|$)`)
)

func ExtractCodeFromName(name string) []string {
	base := javNameBase(name)
	var out []string
	seen := make(map[string]struct{})

	appendUniqueCodes(&out, seen, extractCensoredCodesFromName(base))
	appendUniqueCodes(&out, seen, extractUncensoredCodesFromName(base))
	return out
}

func ExtractUncensoredCodesFromName(name string) []string {
	base := javNameBase(name)
	return extractUncensoredCodesFromName(base)
}

// javNameBase 去掉文件名中的媒体扩展名，作为番号解析的输入。
//
// 常规情况等价于去一次扩展名；STRM 指针文件可能写成 "ABP-123.strm"，
// 也可能发生扩展名重复（如 "ABP-123.mp4.strm"、"ABP-123.strm.mp4"）。
// 需要逐层剥离，直到不再命中视频或 STRM 扩展名，避免残留的 "mp4"/"strm"
// 片段被当成番号后缀而影响匹配。
func javNameBase(name string) string {
	base := filepath.Base(name)
	for i := 0; i < 4; i++ {
		ext := strings.ToLower(filepath.Ext(base))
		if ext == "" {
			break
		}
		if !hasVideoExtension(ext) && ext != StrmExtension {
			break
		}
		stripped := strings.TrimSuffix(base, filepath.Ext(base))
		if strings.TrimSpace(stripped) == "" {
			break
		}
		base = stripped
	}
	return stripFormatTag(base)
}

// bracketFormatTagRe 匹配紧跟在番号后的格式标记，例如 "miaa-131.(mkv)" 中的 "(mkv)"、
// "[1080p]"、"{h265}"。这类标记是命名习惯，不是番号的一部分。
var bracketFormatTagRe = regexp.MustCompile(`\s*[\(\[\{]\s*(?:[0-9]{3,4}[pPiI]|4[kK]|8[kK]|h\.?26[45]|hevc|avc|x26[45]|mkv|mp4|avi|wmv|mov|ts|m2ts|rmvb?|flv|webm|dvd|bd|hd|fhd|uhd|uncensored|leak(?:ed)?|无码|無碼|高清|中字)\s*[\)\]\}]\s*`)

// stripFormatTag 去掉与番号相邻的括号格式标记，避免其被正则吞进番号后缀。
func stripFormatTag(base string) string {
	cleaned := bracketFormatTagRe.ReplaceAllString(base, " ")
	cleaned = strings.TrimSpace(cleaned)
	if cleaned == "" {
		return strings.TrimSpace(base)
	}
	return cleaned
}

func extractFC2PPVCodes(base string) []string {
	var out []string
	seen := make(map[string]struct{})
	for _, match := range fc2PPVCodeRe.FindAllStringSubmatch(base, -1) {
		appendUniqueCode(&out, seen, "FC2-PPV-"+match[2])
	}
	return out
}

func extractUncensoredCodesFromName(base string) []string {
	if codes := extractFC2PPVCodes(base); len(codes) > 0 {
		return codes
	}
	var out []string
	seen := make(map[string]struct{})

	for _, m := range mixedPrefixUncensoredRe.FindAllStringSubmatch(base, -1) {
		if len(m) < 4 {
			continue
		}
		appendUniqueCode(&out, seen, fmt.Sprintf("%s-%s", strings.TrimSpace(m[2]), strings.TrimSpace(m[3])))
	}

	for _, m := range alphaNumericUncensoredRe.FindAllStringSubmatch(base, -1) {
		if len(m) < 5 {
			continue
		}
		prefix := normalizeUncensoredAlphaPrefix(m[2])
		separator := strings.TrimSpace(m[3])
		number := strings.TrimSpace(m[4])
		if separator == "" {
			appendUniqueCode(&out, seen, prefix+number)
		}
		if separator != "" || len(prefix) > 1 {
			appendUniqueCode(&out, seen, fmt.Sprintf("%s-%s", prefix, number))
		}
	}

	for _, m := range pureNumericUncensoredCodeRe.FindAllStringSubmatch(base, -1) {
		if len(m) < 3 {
			continue
		}
		appendUniqueCode(&out, seen, strings.TrimSpace(m[2]))
	}

	for _, m := range explicitShortCodeRe.FindAllStringSubmatch(base, -1) {
		if len(m) < 5 {
			continue
		}
		prefix := strings.ToUpper(strings.TrimSpace(m[2]))
		number := strings.ToUpper(strings.TrimSpace(m[3]))
		suffix := strings.ToUpper(strings.TrimSpace(m[4]))
		base := fmt.Sprintf("%s-%s", prefix, number)
		appendUniqueCode(&out, seen, base)
		if suffix != "" {
			appendUniqueCode(&out, seen, base+suffix)
		}
	}
	return out
}

func normalizeUncensoredAlphaPrefix(prefix string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return ""
	}
	if len(prefix) == 1 {
		return strings.ToLower(prefix)
	}
	prefix = strings.ToLower(prefix)
	return strings.ToUpper(prefix[:1]) + prefix[1:]
}

func extractCensoredCodesFromName(base string) []string {
	// FC2 belongs to uncensored extraction; generic patterns would produce
	// truncated PPV numbers and unrelated codes from site-name prefixes.
	if fc2PPVCodeRe.MatchString(base) {
		return nil
	}
	var out []string
	seen := make(map[string]struct{})

	matches := CodeRe.FindAllStringSubmatch(base, -1)
	for _, m := range matches {
		if len(m) < 3 {
			continue
		}
		suffix := strings.ToUpper(strings.TrimSpace(m[3]))
		number := normalizeNumber(m[2])
		base := fmt.Sprintf("%s-%s", strings.ToUpper(m[1]), number)
		appendUniqueCode(&out, seen, base)
		if suffix != "" {
			appendUniqueCode(&out, seen, base+suffix)
		}
	}
	for _, m := range mixedPrefixCensoredRe.FindAllStringSubmatch(base, -1) {
		if len(m) < 5 {
			continue
		}
		suffix := strings.ToUpper(strings.TrimSpace(m[4]))
		number := normalizeNumber(m[3])
		base := fmt.Sprintf("%s-%s", strings.ToUpper(m[2]), number)
		appendUniqueCode(&out, seen, base)
		if suffix != "" {
			appendUniqueCode(&out, seen, base+suffix)
		}
	}
	return out
}

func appendUniqueCodes(out *[]string, seen map[string]struct{}, codes []string) {
	for _, code := range codes {
		appendUniqueCode(out, seen, code)
	}
}

func appendUniqueCode(out *[]string, seen map[string]struct{}, code string) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if code == "" {
		return
	}
	if _, ok := seen[code]; ok {
		return
	}
	seen[code] = struct{}{}
	*out = append(*out, code)
}

// normalizeNumber trims leading zeros but keeps at least three digits (padding back if needed).
func normalizeNumber(num string) string {
	num = strings.TrimLeft(num, "0")
	if len(num) == 0 {
		num = "0"
	}
	if len(num) < 3 {
		num = fmt.Sprintf("%0*s", 3, num)
	}
	return strings.ToUpper(num)
}
