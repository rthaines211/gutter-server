package server

import (
	"image"
	"image/jpeg"
	_ "image/png"
	"io"
	"os"

	"golang.org/x/image/draw"
)

const (
	thumbnailWidth   = 300
	thumbnailQuality = 70
)

// GenerateThumbnail reads an image from src, resizes it to thumbnailWidth pixels wide
// (maintaining aspect ratio), and saves as JPEG to destPath.
func GenerateThumbnail(src io.Reader, destPath string) error {
	srcImg, _, err := image.Decode(src)
	if err != nil {
		return err
	}

	srcBounds := srcImg.Bounds()
	srcW := srcBounds.Dx()
	srcH := srcBounds.Dy()

	dstW := thumbnailWidth
	dstH := srcH * dstW / srcW

	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), srcImg, srcBounds, draw.Over, nil)

	f, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer f.Close()

	return jpeg.Encode(f, dst, &jpeg.Options{Quality: thumbnailQuality})
}
