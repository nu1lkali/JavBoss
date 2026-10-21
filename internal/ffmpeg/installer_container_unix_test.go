//go:build !windows

package ffmpeg

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestDockerImageSymlinkedFFmpegIsDetected covers the image layout created by
// the Dockerfile: /app/internal/bin/ffmpeg is a symlink to the distribution
// FFmpeg installed with apk, so the resolved path never equals the fixed image
// path. The status must still report the tool as installed.
func TestDockerImageSymlinkedFFmpegIsDetected(t *testing.T) {
	t.Setenv("JAVBOSS_CONTAINER", "1")
	t.Setenv("JAVBOSS_BUILD_MODE", "release")

	imageDir := t.TempDir()
	systemBinary := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(systemBinary, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write system ffmpeg: %v", err)
	}
	imageBinary := filepath.Join(imageDir, "ffmpeg")
	if err := os.Symlink(systemBinary, imageBinary); err != nil {
		t.Fatalf("symlink image ffmpeg: %v", err)
	}

	manager := &installer{
		context:       context.Background(),
		targetPath:    filepath.Join(t.TempDir(), "data", "tools", "linux-x86_64", "ffmpeg"),
		bundledDir:    imageDir,
		containerMode: true,
		downloadURL:   "",
		resolveFFmpeg: func() (string, error) { return imageBinary, nil },
	}

	installed, source, upgrade := manager.detectInstallation()
	if !installed || source != "builtin" || upgrade {
		t.Fatalf("detectInstallation() = %t, %q, %t; want true, builtin, false", installed, source, upgrade)
	}

	if err := os.Remove(imageBinary); err != nil {
		t.Fatalf("remove image ffmpeg: %v", err)
	}
	installed, source, upgrade = manager.detectInstallation()
	if installed || source != "" || upgrade {
		t.Fatalf("detectInstallation() without binary = %t, %q, %t; want false, empty, false", installed, source, upgrade)
	}
}
