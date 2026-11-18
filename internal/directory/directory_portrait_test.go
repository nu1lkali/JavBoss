package directory

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"testing"

	"javboss/internal/models"
)

func TestPortraitCropMatchesCard(t *testing.T) {
	for _, tc := range []struct {
		name     string
		size     image.Rectangle
		position float64
		want     image.Rectangle
	}{
		{"right", image.Rect(0, 0, 800, 538), 1, image.Rect(424, 0, 800, 538)},
		{"left", image.Rect(0, 0, 800, 538), 0, image.Rect(0, 0, 376, 538)},
		{"center", image.Rect(0, 0, 800, 538), 0.5, image.Rect(212, 0, 588, 538)},
		{"tall centered vertically", image.Rect(0, 0, 200, 800), 1, image.Rect(0, 257, 200, 543)},
		{"offset bounds", image.Rect(10, 20, 810, 558), 1, image.Rect(434, 20, 810, 558)},
		{"invalid defaults right", image.Rect(0, 0, 800, 538), math.NaN(), image.Rect(424, 0, 800, 538)},
		{"clamp left", image.Rect(0, 0, 800, 538), -1, image.Rect(0, 0, 376, 538)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := portraitCropBounds(tc.size, tc.position); got != tc.want {
				t.Fatalf("crop = %v, want %v", got, tc.want)
			}
		})
	}
}

func writePortraitTestCover(t *testing.T, dir, extension string) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 800, 538))
	for i, c := range []color.RGBA{{R: 255, A: 255}, {G: 255, A: 255}, {B: 255, A: 255}} {
		draw.Draw(img, image.Rect(i*800/3, 0, (i+1)*800/3, 538), &image.Uniform{C: c}, image.Point{}, draw.Src)
	}
	var data bytes.Buffer
	var err error
	if extension == ".png" {
		encoder := png.Encoder{CompressionLevel: png.NoCompression}
		err = encoder.Encode(&data, img)
	} else {
		err = jpeg.Encode(&data, img, &jpeg.Options{Quality: 95})
	}
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "ipx-001"+extension)
	writeTestFile(t, path, data.Bytes())
	return path
}

// TestDirectoryProcessingWritesPosterAndHonorsFanartSwitch 覆盖上游新增的两个整理
// 开关：exportFanart 决定是否额外导出完整封套；海报本身只要缺失就写，与开关无关。
func TestDirectoryProcessingWritesPosterAndHonorsFanartSwitch(t *testing.T) {
	coverDir := t.TempDir()
	coverPath := writePortraitTestCover(t, coverDir, ".png")
	for _, tc := range []struct {
		name         string
		mode         string
		exportFanart bool
	}{
		{"sidecar without fanart", DirectoryProcessSidecar, false},
		{"sidecar with fanart", DirectoryProcessSidecar, true},
		{"organize and export", DirectoryProcessOrganizeWithSidecar, true},
		{"organize only", DirectoryProcessOrganize, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, filepath.Join(root, "video.mp4"), []byte("video"))
			item := models.Jav{Code: "IPX-001", Videos: []models.Video{{Path: "video.mp4"}}}
			summary := &DirectoryProcessSummary{}
			processJavItem(t.Context(), root, &item, tc.mode, DirectoryProcessLayoutCode, coverDir, summary, false, "媒体库", tc.exportFanart)
			if summary.Failed != 0 {
				t.Fatalf("processing failed: %+v", summary)
			}
			target := root
			if tc.mode != DirectoryProcessSidecar {
				target = filepath.Join(root, "媒体库", "IPX-001")
			}
			posterPath := filepath.Join(target, "video-poster.png")
			if tc.mode == DirectoryProcessOrganize {
				if _, err := os.Stat(posterPath); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("organize-only must not write sidecars: %v", err)
				}
				return
			}
			data, err := os.ReadFile(posterPath)
			if err != nil {
				t.Fatal(err)
			}
			img, err := jpeg.Decode(bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			// 800x538 的封面裁掉左侧 53%（424px）后应剩 376x538。
			if img.Bounds() != image.Rect(0, 0, 376, 538) {
				t.Fatalf("unexpected poster size: %v", img.Bounds())
			}
			fanartPath := filepath.Join(target, "video-fanart.png")
			fanart, err := os.ReadFile(fanartPath)
			if tc.exportFanart {
				original, readErr := os.ReadFile(coverPath)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if err != nil || !bytes.Equal(fanart, original) {
					t.Fatalf("fanart must be the untouched full cover: %v", err)
				}
				return
			}
			if !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("fanart must not be exported when disabled: %v", err)
			}
		})
	}
}

// TestSidecarArtworkNamesAndContent 锁定旁注文件的命名与内容：
// 海报按封面扩展名命名且是被裁剪过的竖版图，背景图是封面原样副本。
func TestSidecarArtworkNamesAndContent(t *testing.T) {
	for _, extension := range []string{".jpg", ".png"} {
		for _, exportFanart := range []bool{false, true} {
			root, coverDir := t.TempDir(), t.TempDir()
			coverPath := writePortraitTestCover(t, coverDir, extension)
			video := filepath.Join(root, "video.mp4")
			writeTestFile(t, video, []byte("video"))
			result, err := writeJavSidecars(t.Context(), video, &models.Jav{Code: "IPX-001"}, coverDir, false, exportFanart)
			if err != nil {
				t.Fatal(err)
			}
			if !result.posterWritten {
				t.Fatalf("result = %+v, want the poster written", result)
			}
			original, err := os.ReadFile(coverPath)
			if err != nil {
				t.Fatal(err)
			}
			fanart, err := os.ReadFile(filepath.Join(root, "video-fanart"+extension))
			if exportFanart {
				if err != nil || !bytes.Equal(fanart, original) {
					t.Fatalf("fanart must contain the unchanged full cover: %v", err)
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("fanart must not be exported when disabled: %v", err)
			}
			poster, err := os.ReadFile(filepath.Join(root, "video-poster"+extension))
			if err != nil {
				t.Fatal(err)
			}
			img, err := jpeg.Decode(bytes.NewReader(poster))
			if err != nil || img.Bounds() != image.Rect(0, 0, 376, 538) {
				t.Fatalf("poster must contain the cropped portrait: %v", err)
			}
		}
	}
}

// TestSidecarPosterIsNeverOverwritten 锁定 fork 的核心约束：
// 视频旁已有非空海报时，无论 NFO 归属谁都不要再动它。
func TestSidecarPosterIsNeverOverwritten(t *testing.T) {
	for _, managed := range []bool{false, true} {
		root, coverDir := t.TempDir(), t.TempDir()
		writePortraitTestCover(t, coverDir, ".jpg")
		video := filepath.Join(root, "video.mp4")
		poster := filepath.Join(root, "video-poster.jpg")
		writeTestFile(t, video, []byte("video"))
		writeTestFile(t, poster, []byte("original portrait"))
		if managed {
			writeTestFile(t, filepath.Join(root, "video.nfo"), []byte("<generator>JavBoss</generator>"))
		}
		if _, err := writeJavSidecars(t.Context(), video, &models.Jav{Code: "IPX-001", CoverCropPosition: 1}, coverDir, false, false); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(poster)
		if err != nil || string(data) != "original portrait" {
			t.Fatalf("existing poster changed: %q %v", data, err)
		}
	}

	// 无效封面不应破坏已经存在的目标文件。
	root := t.TempDir()
	invalid := filepath.Join(root, "bad.jpg")
	target := filepath.Join(root, "video-poster.jpg")
	writeTestFile(t, invalid, []byte("not an image"))
	writeTestFile(t, target, []byte("keep"))
	if err := writePortraitSidecar(invalid, target, 1); err == nil {
		t.Fatal("invalid cover should fail")
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "keep" {
		t.Fatalf("failed export changed the target: %q, %v", data, err)
	}
}

func TestPortraitSidecarDecodesWebP(t *testing.T) {
	// An 8x4 solid blue lossless WebP, generated with FFmpeg.
	data, err := base64.StdEncoding.DecodeString("UklGRhwAAABXRUJQVlA4TA8AAAAvB8AAAAcQ0f/+ByKi/wEA")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	source, target := filepath.Join(root, "cover.webp"), filepath.Join(root, "video-poster.jpg")
	writeTestFile(t, source, data)
	if err := writePortraitSidecar(source, target, 1); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil || img.Bounds() != image.Rect(0, 0, 3, 4) {
		t.Fatalf("WebP portrait export failed: %v", err)
	}
}
