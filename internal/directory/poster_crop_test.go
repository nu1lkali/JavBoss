package directory

import (
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"

	"javboss/internal/models"
)

func TestPosterCropFractionDefaultsAndOverrides(t *testing.T) {
	tests := []struct {
		name string
		env  string
		set  bool
		want float64
	}{
		{name: "unset uses default", want: defaultPosterCropLeft},
		{name: "zero disables cropping", env: "0", set: true, want: 0},
		{name: "explicit fraction", env: "0.4", set: true, want: 0.4},
		{name: "negative disables cropping", env: "-1", set: true, want: 0},
		{name: "above maximum is clamped", env: "0.99", set: true, want: maxPosterCropLeft},
		{name: "invalid falls back to default", env: "abc", set: true, want: defaultPosterCropLeft},
		{name: "empty string uses default", env: "   ", set: true, want: defaultPosterCropLeft},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv("JAVBOSS_POSTER_CROP_LEFT", tc.env)
			} else {
				t.Setenv("JAVBOSS_POSTER_CROP_LEFT", "")
			}
			if got := posterCropFraction(); got != tc.want {
				t.Fatalf("posterCropFraction() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestWriteCroppedPosterRemovesLeftPortion(t *testing.T) {
	dir := t.TempDir()
	coverPath := filepath.Join(dir, "cover.jpg")
	posterPath := filepath.Join(dir, "video-poster.jpg")

	// 200x300 的封面，左侧 53%（106px）应被裁掉，输出应为 94x300。
	const width, height = 200, 300
	src := image.NewRGBA(image.Rect(0, 0, width, height))
	for x := 0; x < width; x++ {
		for y := 0; y < height; y++ {
			fill := color.RGBA{R: 10, G: 20, B: 30, A: 255}
			if x >= 106 {
				fill = color.RGBA{R: 200, G: 100, B: 50, A: 255}
			}
			src.Set(x, y, fill)
		}
	}
	file, err := os.Create(coverPath)
	if err != nil {
		t.Fatalf("create cover: %v", err)
	}
	if err := jpeg.Encode(file, src, nil); err != nil {
		file.Close()
		t.Fatalf("encode cover: %v", err)
	}
	file.Close()

	ok, err := writeCroppedPoster(coverPath, posterPath, defaultPosterCropLeft)
	if err != nil {
		t.Fatalf("writeCroppedPoster returned error: %v", err)
	}
	if !ok {
		t.Fatal("expected crop to be applied")
	}

	written, err := os.Open(posterPath)
	if err != nil {
		t.Fatalf("open poster: %v", err)
	}
	defer written.Close()
	img, err := jpeg.Decode(written)
	if err != nil {
		t.Fatalf("decode poster: %v", err)
	}
	b := img.Bounds()
	if b.Dx() != 94 || b.Dy() != height {
		t.Fatalf("poster size = %dx%d, want 94x%d", b.Dx(), b.Dy(), height)
	}
	// 裁掉左半后，左上角像素应来自原图右侧区域。
	r, g, bl, _ := img.At(b.Min.X, b.Min.Y).RGBA()
	if r>>8 < 150 || g>>8 < 60 {
		t.Fatalf("poster pixel = (%d,%d,%d), want right-side pixel", r>>8, g>>8, bl>>8)
	}
}

func TestWriteCroppedPosterSkipsWhenCropDisabled(t *testing.T) {
	dir := t.TempDir()
	coverPath := filepath.Join(dir, "cover.jpg")
	posterPath := filepath.Join(dir, "video-poster.jpg")

	img := image.NewRGBA(image.Rect(0, 0, 100, 100))
	file, err := os.Create(coverPath)
	if err != nil {
		t.Fatalf("create cover: %v", err)
	}
	if err := jpeg.Encode(file, img, nil); err != nil {
		file.Close()
		t.Fatalf("encode cover: %v", err)
	}
	file.Close()

	ok, err := writeCroppedPoster(coverPath, posterPath, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected no crop when fraction is zero")
	}
	if _, statErr := os.Stat(posterPath); !os.IsNotExist(statErr) {
		t.Fatalf("poster should not be created, stat err = %v", statErr)
	}
}

func TestWriteJavSidecarsUsesVideoNameForImages(t *testing.T) {
	// 海报/背景图带视频名（{name}-poster.jpg / {name}-fanart.jpg）：
	// Emby 的 Primary 图片优先序列里 {name}-poster.ext 排在裸名 poster.ext 之前，
	// 一个目录放多部影片或多个版本时也不会互相覆盖。
	// 视频名本身带 "(mp4)" 这类格式标记时，图片名也应完整沿用，不做归一化。
	dir := t.TempDir()
	coverDir := filepath.Join(dir, "cover")
	videoDir := filepath.Join(dir, "av", "MIGD-641")
	videoPath := filepath.Join(videoDir, "MIGD-641.(mkv).strm")

	if err := os.MkdirAll(coverDir, 0o755); err != nil {
		t.Fatalf("create cover dir: %v", err)
	}
	if err := os.MkdirAll(videoDir, 0o755); err != nil {
		t.Fatalf("create video dir: %v", err)
	}
	if err := os.WriteFile(videoPath, []byte("http://example.test/migd-641.mkv"), 0o644); err != nil {
		t.Fatalf("write strm: %v", err)
	}

	cached := filepath.Join(coverDir, "migd-641.jpg")
	// 封面缓存要求大于 5KB 才算有效，纯色图会被判为占位图，这里用噪点图。
	writeTestJPEG(t, cached, 200, 300)

	result, err := writeJavSidecars(context.Background(), videoPath, &models.Jav{Code: "MIGD-641"}, coverDir)
	if err != nil {
		t.Fatalf("writeJavSidecars returned error: %v", err)
	}
	if !result.posterWritten || !result.fanartWritten {
		t.Fatalf("result = %+v, want both poster and fanart written", result)
	}

	for _, name := range []string{
		"MIGD-641.(mkv).nfo",
		"MIGD-641.(mkv)-poster.jpg",
		"MIGD-641.(mkv)-fanart.jpg",
	} {
		if _, statErr := os.Stat(filepath.Join(videoDir, name)); statErr != nil {
			t.Fatalf("%s missing next to the video: %v", name, statErr)
		}
	}
	// 确认写出的海报是裁过左侧的竖版图，而不是整幅封套。
	poster, err := os.Open(filepath.Join(videoDir, "MIGD-641.(mkv)-poster.jpg"))
	if err != nil {
		t.Fatalf("open poster: %v", err)
	}
	defer poster.Close()
	decoded, err := jpeg.Decode(poster)
	if err != nil {
		t.Fatalf("decode poster: %v", err)
	}
	if b := decoded.Bounds(); b.Dx() != 94 || b.Dy() != 300 {
		t.Fatalf("poster size = %dx%d, want 94x300 (cropped)", b.Dx(), b.Dy())
	}
	// 不允许写成裸名。
	for _, name := range []string{"poster.jpg", "fanart.jpg"} {
		if _, statErr := os.Stat(filepath.Join(videoDir, name)); !os.IsNotExist(statErr) {
			t.Fatalf("%s should not exist, stat err = %v", name, statErr)
		}
	}
}

func TestWriteJavFanartCopiesFullCoverBesideVideo(t *testing.T) {
	dir := t.TempDir()
	coverPath := filepath.Join(dir, "cover.jpg")
	base := filepath.Join(dir, "ABP-123")

	// 封面用 200x300 的整幅图，fanart 应保持原始尺寸（不裁剪）。
	img := image.NewRGBA(image.Rect(0, 0, 200, 300))
	file, err := os.Create(coverPath)
	if err != nil {
		t.Fatalf("create cover: %v", err)
	}
	if err := jpeg.Encode(file, img, nil); err != nil {
		file.Close()
		t.Fatalf("encode cover: %v", err)
	}
	file.Close()

	var result sidecarResult
	writeJavFanart(context.Background(), base, &models.Jav{Code: "ABP-123"}, coverPath, &result)

	if !result.fanartWritten {
		t.Fatal("expected fanart to be written")
	}
	fanartPath := filepath.Join(dir, "ABP-123-fanart.jpg")
	fanartFile, err := os.Open(fanartPath)
	if err != nil {
		t.Fatalf("open fanart: %v", err)
	}
	defer fanartFile.Close()
	decoded, err := jpeg.Decode(fanartFile)
	if err != nil {
		t.Fatalf("decode fanart: %v", err)
	}
	b := decoded.Bounds()
	if b.Dx() != 200 || b.Dy() != 300 {
		t.Fatalf("fanart size = %dx%d, want 200x300 (uncropped)", b.Dx(), b.Dy())
	}
}

func TestWriteJavFanartRespectsDisableSwitch(t *testing.T) {
	dir := t.TempDir()
	coverPath := filepath.Join(dir, "cover.jpg")
	base := filepath.Join(dir, "ABP-123")

	img := image.NewRGBA(image.Rect(0, 0, 100, 100))
	file, err := os.Create(coverPath)
	if err != nil {
		t.Fatalf("create cover: %v", err)
	}
	if err := jpeg.Encode(file, img, nil); err != nil {
		file.Close()
		t.Fatalf("encode cover: %v", err)
	}
	file.Close()

	t.Setenv("JAVBOSS_POSTER_FANART", "0")
	var result sidecarResult
	writeJavFanart(context.Background(), base, &models.Jav{Code: "ABP-123"}, coverPath, &result)

	if result.fanartWritten {
		t.Fatal("fanart should not be written when disabled")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "ABP-123-fanart.jpg")); !os.IsNotExist(statErr) {
		t.Fatalf("fanart file should not exist, stat err = %v", statErr)
	}
}

func TestWriteJavFanartKeepsExistingFile(t *testing.T) {
	dir := t.TempDir()
	coverPath := filepath.Join(dir, "cover.jpg")
	base := filepath.Join(dir, "ABP-123")
	existing := filepath.Join(dir, "ABP-123-fanart.jpg")

	img := image.NewRGBA(image.Rect(0, 0, 100, 100))
	file, err := os.Create(coverPath)
	if err != nil {
		t.Fatalf("create cover: %v", err)
	}
	if err := jpeg.Encode(file, img, nil); err != nil {
		file.Close()
		t.Fatalf("encode cover: %v", err)
	}
	file.Close()

	if err := os.WriteFile(existing, []byte("existing-fanart"), 0o644); err != nil {
		t.Fatalf("seed fanart: %v", err)
	}

	var result sidecarResult
	writeJavFanart(context.Background(), base, &models.Jav{Code: "ABP-123"}, coverPath, &result)

	if result.fanartWritten {
		t.Fatal("existing fanart should not be overwritten")
	}
	data, err := os.ReadFile(existing)
	if err != nil {
		t.Fatalf("read fanart: %v", err)
	}
	if string(data) != "existing-fanart" {
		t.Fatalf("fanart was overwritten: %q", data)
	}
}
