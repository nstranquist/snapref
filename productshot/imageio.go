package productshot

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png"
	"os"
)

// DecodeImage reads PNG or JPEG bytes.
func DecodeImage(data []byte) (image.Image, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode capture: %w", err)
	}
	return img, nil
}

// DecodeImageFile reads an image from disk.
func DecodeImageFile(path string) (image.Image, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return DecodeImage(data)
}

// WriteJPEG writes a JPEG at quality q (1-100).
func WriteJPEG(path string, img image.Image, q int) error {
	if q <= 0 {
		q = 80
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	err = jpeg.Encode(f, img, &jpeg.Options{Quality: q})
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}
