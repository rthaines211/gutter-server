//go:build ignore

package main

import (
	"archive/zip"
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"os"
)

// minimalPNG creates a tiny valid PNG (1x1 pixel, grayscale)
func minimalPNG() []byte {
	// PNG signature
	sig := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}

	writeChunk := func(buf *bytes.Buffer, name string, data []byte) {
		binary.Write(buf, binary.BigEndian, uint32(len(data)))
		buf.WriteString(name)
		buf.Write(data)
		// CRC32 of name+data (simplified: use 0 for test fixture)
		crc := crc32PNG([]byte(name), data)
		binary.Write(buf, binary.BigEndian, crc)
	}

	var buf bytes.Buffer
	buf.Write(sig)

	// IHDR: 1x1 pixel, 8-bit grayscale
	ihdr := []byte{
		0, 0, 0, 1, // width = 1
		0, 0, 0, 1, // height = 1
		8, // bit depth
		0, // color type: grayscale
		0, // compression
		0, // filter
		0, // interlace
	}
	writeChunk(&buf, "IHDR", ihdr)

	// IDAT: compressed image data (filter byte 0x00 + 1 byte pixel 0x00)
	var zlibBuf bytes.Buffer
	w := zlib.NewWriter(&zlibBuf)
	w.Write([]byte{0x00, 0x00}) // filter=none, pixel=0
	w.Close()
	writeChunk(&buf, "IDAT", zlibBuf.Bytes())

	// IEND
	writeChunk(&buf, "IEND", []byte{})

	return buf.Bytes()
}

func crc32PNG(name []byte, data []byte) uint32 {
	// CRC32 table
	var table [256]uint32
	for i := 0; i < 256; i++ {
		c := uint32(i)
		for k := 0; k < 8; k++ {
			if c&1 != 0 {
				c = 0xedb88320 ^ (c >> 1)
			} else {
				c >>= 1
			}
		}
		table[i] = c
	}
	crc := uint32(0xffffffff)
	for _, b := range name {
		crc = table[(crc^uint32(b))&0xff] ^ (crc >> 8)
	}
	for _, b := range data {
		crc = table[(crc^uint32(b))&0xff] ^ (crc >> 8)
	}
	return crc ^ 0xffffffff
}

func main() {
	f, err := os.Create("test.cbz")
	if err != nil {
		panic(err)
	}
	defer f.Close()

	w := zip.NewWriter(f)
	defer w.Close()

	png := minimalPNG()
	for _, name := range []string{"page000.png", "page001.png", "page002.png"} {
		fw, err := w.Create(name)
		if err != nil {
			panic(err)
		}
		fw.Write(png)
	}

	xmlContent := `<?xml version="1.0" encoding="utf-8"?>
<ComicInfo>
  <Title>Test Issue</Title>
  <Series>Test Series</Series>
  <Number>1</Number>
  <Writer>Test Writer</Writer>
  <Penciller>Test Artist</Penciller>
  <Summary>A test comic.</Summary>
  <PageCount>3</PageCount>
</ComicInfo>`

	fw, err := w.Create("ComicInfo.xml")
	if err != nil {
		panic(err)
	}
	fw.Write([]byte(xmlContent))
}
