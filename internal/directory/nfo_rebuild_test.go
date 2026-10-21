package directory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"javboss/internal/models"
)

// 真实样本：别的刮削器写出来的空壳 NFO，只有番号和 runtime。
const shellNFOSample = `<?xml version="1.0" encoding="utf-8" standalone="yes"?>
<movie>
<plot />
<outline />
<lockdata>false</lockdata>
<dateadded>2026-10-15 23:02:29</dateadded>
<title>ZOZO-191</title>
<sorttitle>ZOZO-191</sorttitle>
<runtime>108</runtime>
<fileinfo>
<streamdetails />
</fileinfo>
</movie>`

// 带正文的外部 NFO，必须保留。
const richNFOSample = `<?xml version="1.0" encoding="utf-8" standalone="yes"?>
<movie>
<plot><![CDATA[椎名ほのか先生の検査]]></plot>
<title>ZOZO-048 椎名ほのか先生</title>
<genre>女教師</genre>
</movie>`

const ownedNFOSample = `<?xml version="1.0" encoding="utf-8"?>
<movie>
  <generator>JavBoss</generator>
  <title>ZOZO-191</title>
</movie>`

func TestInspectNFOClassifiesStates(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return path
	}

	cases := []struct {
		name string
		path string
		want NFOInspectState
	}{
		{"missing", filepath.Join(dir, "absent.nfo"), NFOMissing},
		{"empty file", write("empty.nfo", ""), NFOShell},
		{"foreign shell", write("shell.nfo", shellNFOSample), NFOShell},
		{"foreign rich", write("rich.nfo", richNFOSample), NFORich},
		{"javboss owned", write("owned.nfo", ownedNFOSample), NFOOwnedByJavBoss},
		{"genre only", write("genre.nfo", `<movie><genre>単体作品</genre></movie>`), NFORich},
		{"actor only", write("actor.nfo", `<movie><actor><name>椎名ほのか</name></actor></movie>`), NFORich},
		// 解析不了的 NFO 一律当成别人的完整文件，绝不覆盖。
		{"malformed", write("broken.nfo", `<movie><plot>未闭合`), NFORich},
	}

	for _, tc := range cases {
		got, err := inspectNFO(tc.path)
		if err != nil {
			t.Fatalf("%s: inspectNFO returned error: %v", tc.name, err)
		}
		if got != tc.want {
			t.Fatalf("%s: inspectNFO = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestJavHasMetadata(t *testing.T) {
	if javHasMetadata(nil) {
		t.Fatal("nil item must not be treated as having metadata")
	}
	if javHasMetadata(&models.Jav{Code: "ZOZO-191"}) {
		t.Fatal("record with only a code must not be rebuilt")
	}
	// 时长来自视频文件本身，不代表刮削成功，写出来还是空壳。
	if javHasMetadata(&models.Jav{Code: "ZOZO-191", DurationMin: 108}) {
		t.Fatal("record with only a duration must not be rebuilt")
	}
	cases := map[string]*models.Jav{
		"title":    {Code: "A-1", Title: "タイトル"},
		"zh title": {Code: "A-1", ZhTitle: "中文标题"},
		"studio":   {Code: "A-1", Studio: &models.JavStudio{Name: "Studio"}},
		"series":   {Code: "A-1", Series: &models.JavSeries{Name: "Series"}},
		"release":  {Code: "A-1", ReleaseUnix: 1710000000},
		"tags":     {Code: "A-1", Tags: []models.JavTag{{Name: "単体作品"}}},
		"idols":    {Code: "A-1", Idols: []models.JavIdol{{Name: "椎名ほのか"}}},
	}
	for name, item := range cases {
		if !javHasMetadata(item) {
			t.Fatalf("%s: javHasMetadata = false, want true", name)
		}
	}
}

// 重建后的 NFO 必须能被再次识别为 JavBoss 自己写的，否则下一轮又会被当成空壳。
func TestMakeJellyfinNFOProducesOwnedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ZOZO-048.nfo")
	item := &models.Jav{
		Code:        "ZOZO-048",
		Title:       "椎名ほのか先生",
		DurationMin: 58,
		ReleaseUnix: time.Date(2021, time.February, 5, 0, 0, 0, 0, time.UTC).Unix(),
		Studio:      &models.JavStudio{Name: "サディヴィレナウ！"},
	}
	if err := writeNFOFile(path, makeJellyfinNFO(item)); err != nil {
		t.Fatalf("write NFO: %v", err)
	}
	state, err := inspectNFO(path)
	if err != nil {
		t.Fatalf("inspect rebuilt NFO: %v", err)
	}
	if state != NFOOwnedByJavBoss {
		t.Fatalf("rebuilt NFO state = %v, want NFOOwnedByJavBoss", state)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read rebuilt NFO: %v", err)
	}
	text := string(raw)
	for _, want := range []string{
		"<generator>JavBoss</generator>",
		"<title>ZOZO-048 椎名ほのか先生</title>",
		"<originaltitle>椎名ほのか先生</originaltitle>",
		"<premiered>2021-02-05</premiered>",
		"<year>2021</year>",
		"<tagline>配信開始日 2021-02-05</tagline>",
		"<outline>配信開始日 2021-02-05</outline>",
		"<mpaa>JP-18+</mpaa>",
		"<studio>サディヴィレナウ！</studio>",
		"<runtime>58</runtime>",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("rebuilt NFO missing %q:\n%s", want, text)
		}
	}
}
