// Command imgtool converts the PNG artwork into the formats Windows tools need.
//
//	go run ./packaging/imgtool ico -o Backplane.ico icon-16.png icon-32.png ...
//	go run ./packaging/imgtool bmp -o welcome.bmp welcome.png
//
// ico packs PNGs into a .ico: images smaller than 256 px are stored as 32-bit
// bitmaps (readable by every Windows tool, including NSIS) and the 256 px
// image as PNG, as Windows expects. bmp writes a 24-bit bitmap, which is what
// NSIS wants for installer artwork.
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	out := fs.String("o", "", "output file")
	_ = fs.Parse(os.Args[2:])
	if *out == "" || fs.NArg() == 0 {
		usage()
	}
	switch os.Args[1] {
	case "ico":
		writeICO(*out, fs.Args())
	case "bmp":
		if fs.NArg() != 1 {
			usage()
		}
		writeBMP(*out, fs.Arg(0))
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: imgtool ico -o out.ico image.png...\n       imgtool bmp -o out.bmp image.png")
	os.Exit(2)
}

func writeICO(out string, paths []string) {
	type entry struct {
		w, h int
		data []byte
	}
	var entries []entry
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			fail(err)
		}
		img, err := png.Decode(bytes.NewReader(raw))
		if err != nil {
			fail(fmt.Errorf("%s: %w", path, err))
		}
		b := img.Bounds()
		w, h := b.Dx(), b.Dy()
		if w > 256 || h > 256 {
			fail(fmt.Errorf("%s: %dx%d is larger than 256 px", path, w, h))
		}
		data := raw
		if w < 256 {
			data = bitmap(img)
		}
		entries = append(entries, entry{w, h, data})
	}
	var buf bytes.Buffer
	le := binary.LittleEndian
	_ = binary.Write(&buf, le, [3]uint16{0, 1, uint16(len(entries))})
	offset := 6 + 16*len(entries)
	for _, e := range entries {
		_ = binary.Write(&buf, le, struct {
			W, H, Colors, Reserved uint8
			Planes, Bits           uint16
			Size, Offset           uint32
		}{uint8(e.w % 256), uint8(e.h % 256), 0, 0, 1, 32, uint32(len(e.data)), uint32(offset)})
		offset += len(e.data)
	}
	for _, e := range entries {
		buf.Write(e.data)
	}
	if err := os.WriteFile(out, buf.Bytes(), 0o644); err != nil {
		fail(err)
	}
}

// writeBMP writes a 24-bit bottom-up bitmap; transparent pixels are
// flattened onto the artwork's rack-rail background.
func writeBMP(out, path string) {
	f, err := os.Open(path)
	if err != nil {
		fail(err)
	}
	img, err := png.Decode(f)
	f.Close()
	if err != nil {
		fail(fmt.Errorf("%s: %w", path, err))
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	stride := (w*3 + 3) &^ 3
	size := 54 + stride*h
	var buf bytes.Buffer
	le := binary.LittleEndian
	buf.WriteString("BM")
	_ = binary.Write(&buf, le, []uint32{uint32(size), 0, 54})
	_ = binary.Write(&buf, le, struct {
		Size                  uint32
		Width, Height         int32
		Planes, Bits          uint16
		Compression, ImgSize  uint32
		XPPM, YPPM            int32
		ColorsUsed, Important uint32
	}{40, int32(w), int32(h), 1, 24, 0, uint32(stride * h), 2835, 2835, 0, 0})
	bg := color.NRGBA{0x12, 0x14, 0x16, 0xff}
	for y := h - 1; y >= 0; y-- {
		row := make([]byte, stride)
		for x := 0; x < w; x++ {
			c := color.NRGBAModel.Convert(img.At(b.Min.X+x, b.Min.Y+y)).(color.NRGBA)
			a := int(c.A)
			mix := func(v, back uint8) byte { return byte((int(v)*a + int(back)*(255-a)) / 255) }
			row[x*3], row[x*3+1], row[x*3+2] = mix(c.B, bg.B), mix(c.G, bg.G), mix(c.R, bg.R)
		}
		buf.Write(row)
	}
	if err := os.WriteFile(out, buf.Bytes(), 0o644); err != nil {
		fail(err)
	}
}

// bitmap encodes img as an icon DIB: a BITMAPINFOHEADER with doubled height,
// bottom-up 32-bit BGRA pixels, then the 1-bit AND mask (set = transparent).
func bitmap(img image.Image) []byte {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	maskStride := ((w + 31) / 32) * 4
	var buf bytes.Buffer
	le := binary.LittleEndian
	_ = binary.Write(&buf, le, struct {
		Size                  uint32
		Width, Height         int32
		Planes, Bits          uint16
		Compression, ImgSize  uint32
		XPPM, YPPM            int32
		ColorsUsed, Important uint32
	}{40, int32(w), int32(2 * h), 1, 32, 0, uint32(w*h*4 + maskStride*h), 0, 0, 0, 0})
	for y := h - 1; y >= 0; y-- {
		for x := 0; x < w; x++ {
			c := color.NRGBAModel.Convert(img.At(b.Min.X+x, b.Min.Y+y)).(color.NRGBA)
			buf.Write([]byte{c.B, c.G, c.R, c.A})
		}
	}
	for y := h - 1; y >= 0; y-- {
		row := make([]byte, maskStride)
		for x := 0; x < w; x++ {
			if _, _, _, a := img.At(b.Min.X+x, b.Min.Y+y).RGBA(); a == 0 {
				row[x/8] |= 0x80 >> (x % 8)
			}
		}
		buf.Write(row)
	}
	return buf.Bytes()
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "imgtool:", err)
	os.Exit(1)
}
