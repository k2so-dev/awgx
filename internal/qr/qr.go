package qr

import (
	"strings"

	qrcode "github.com/skip2/go-qrcode"
)

func Terminal(data string) (string, error) {
	q, err := qrcode.New(data, qrcode.Low)
	if err != nil {
		return "", err
	}
	bits := q.Bitmap()
	var b strings.Builder
	for y := 0; y < len(bits); y += 2 {
		for x := range bits[y] {
			top := bits[y][x]
			bottom := y+1 < len(bits) && bits[y+1][x]
			switch {
			case top && bottom:
				b.WriteString(" ")
			case top:
				b.WriteString("▄")
			case bottom:
				b.WriteString("▀")
			default:
				b.WriteString("█")
			}
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

func PNG(data string, size int) ([]byte, error) {
	return qrcode.Encode(data, qrcode.Low, size)
}
