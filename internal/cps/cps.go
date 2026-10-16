package cps

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

type Tag struct {
	Name  string
	Bytes []byte
	N     int
}

func (t Tag) Size() int {
	switch t.Name {
	case "b":
		return len(t.Bytes)
	case "t":
		return 4
	}
	return t.N
}

func (t Tag) String() string {
	switch t.Name {
	case "b":
		return "<b 0x" + hex.EncodeToString(t.Bytes) + ">"
	case "t":
		return "<t>"
	}
	return fmt.Sprintf("<%s %d>", t.Name, t.N)
}

type Packet []Tag

func (p Packet) Size() int {
	n := 0
	for _, t := range p {
		n += t.Size()
	}
	return n
}

func (p Packet) String() string {
	var b strings.Builder
	for _, t := range p {
		b.WriteString(t.String())
	}
	return b.String()
}

const MaxTagLen = 65535

func Parse(s string) (Packet, error) {
	var p Packet
	rest := strings.TrimSpace(s)
	for rest != "" {
		if rest[0] != '<' {
			return nil, fmt.Errorf("unexpected text %q outside of a tag", firstWord(rest))
		}
		end := strings.IndexByte(rest, '>')
		if end < 0 {
			return nil, fmt.Errorf("missing closing > in %q", rest)
		}
		t, err := parseTag(rest[1:end])
		if err != nil {
			return nil, err
		}
		p = append(p, t)
		rest = strings.TrimSpace(rest[end+1:])
	}
	return p, nil
}

func parseTag(body string) (Tag, error) {
	fields := strings.Fields(body)
	if len(fields) == 0 {
		return Tag{}, fmt.Errorf("empty tag")
	}
	name := fields[0]
	args := fields[1:]
	switch name {
	case "t":
		if len(args) != 0 {
			return Tag{}, fmt.Errorf("<t> takes no argument")
		}
		return Tag{Name: name}, nil
	case "b":
		if len(args) != 1 {
			return Tag{}, fmt.Errorf("<b> takes one hex argument")
		}
		h, ok := strings.CutPrefix(args[0], "0x")
		if !ok || h == "" {
			return Tag{}, fmt.Errorf("<b> argument must start with 0x")
		}
		b, err := hex.DecodeString(h)
		if err != nil {
			return Tag{}, fmt.Errorf("<b> argument is not valid hex")
		}
		return Tag{Name: name, Bytes: b}, nil
	case "r", "rc", "rd":
		if len(args) != 1 {
			return Tag{}, fmt.Errorf("<%s> takes one number", name)
		}
		n, err := strconv.Atoi(args[0])
		if err != nil || n <= 0 || n > MaxTagLen {
			return Tag{}, fmt.Errorf("<%s> length must be 1..%d", name, MaxTagLen)
		}
		return Tag{Name: name, N: n}, nil
	}
	return Tag{}, fmt.Errorf("unsupported tag <%s>", name)
}

func Bytes(b []byte) Packet {
	return Packet{{Name: "b", Bytes: b}}
}

func firstWord(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		return f[0]
	}
	return s
}
