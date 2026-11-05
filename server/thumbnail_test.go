package server

import (
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func createTestImage(t *testing.T, path string, width, height int, format string) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: 255, G: 0, B: 0, A: 255})
		}
	}

	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	switch format {
	case "png":
		png.Encode(f, img)
	case "jpeg":
		jpeg.Encode(f, img, &jpeg.Options{Quality: 90})
	}
}

func TestGenerateThumbnail(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.png")
	dstPath := filepath.Join(dir, "thumb.jpg")

	createTestImage(t, srcPath, 1200, 1800, "png")

	src, err := os.Open(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()

	err = GenerateThumbnail(src, dstPath)
	if err != nil {
		t.Fatalf("GenerateThumbnail failed: %v", err)
	}

	if _, err := os.Stat(dstPath); err != nil {
		t.Fatalf("thumbnail file not created: %v", err)
	}

	f, err := os.Open(dstPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	img, err := jpeg.Decode(f)
	if err != nil {
		t.Fatalf("failed to decode thumbnail: %v", err)
	}

	bounds := img.Bounds()
	if bounds.Dx() != 300 {
		t.Errorf("expected width 300, got %d", bounds.Dx())
	}
	if bounds.Dy() != 450 {
		t.Errorf("expected height 450, got %d", bounds.Dy())
	}
}

func TestGenerateThumbnailFromJPEG(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.jpg")
	dstPath := filepath.Join(dir, "thumb.jpg")

	createTestImage(t, srcPath, 800, 1200, "jpeg")

	src, err := os.Open(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()

	err = GenerateThumbnail(src, dstPath)
	if err != nil {
		t.Fatalf("GenerateThumbnail failed: %v", err)
	}

	f, err := os.Open(dstPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	img, err := jpeg.Decode(f)
	if err != nil {
		t.Fatalf("failed to decode thumbnail: %v", err)
	}

	bounds := img.Bounds()
	if bounds.Dx() != 300 {
		t.Errorf("expected width 300, got %d", bounds.Dx())
	}
	if bounds.Dy() != 450 {
		t.Errorf("expected height 450, got %d", bounds.Dy())
	}
}

func TestGenerateThumbnailSmallImage(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.png")
	dstPath := filepath.Join(dir, "thumb.jpg")

	createTestImage(t, srcPath, 200, 300, "png")

	src, err := os.Open(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()

	err = GenerateThumbnail(src, dstPath)
	if err != nil {
		t.Fatalf("GenerateThumbnail failed: %v", err)
	}

	f, err := os.Open(dstPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	img, err := jpeg.Decode(f)
	if err != nil {
		t.Fatalf("failed to decode: %v", err)
	}

	if img.Bounds().Dx() != 300 {
		t.Errorf("expected width 300, got %d", img.Bounds().Dx())
	}
}
