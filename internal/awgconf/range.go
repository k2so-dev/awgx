package awgconf

import (
	"fmt"
	"strconv"
	"strings"
)

type Range struct {
	Lo uint32 `json:"lo"`
	Hi uint32 `json:"hi"`
}

func Single(v uint32) Range {
	return Range{Lo: v, Hi: v}
}

func ParseRange(s string, max uint64) (Range, error) {
	lo, hi, found := strings.Cut(strings.TrimSpace(s), "-")
	if !found {
		hi = lo
	}
	l, err := strconv.ParseUint(lo, 10, 64)
	if err != nil || l > max {
		return Range{}, fmt.Errorf("invalid range %q", s)
	}
	h, err := strconv.ParseUint(hi, 10, 64)
	if err != nil || h > max {
		return Range{}, fmt.Errorf("invalid range %q", s)
	}
	if h < l {
		return Range{}, fmt.Errorf("invalid range %q: max < min", s)
	}
	return Range{Lo: uint32(l), Hi: uint32(h)}, nil
}

func (r Range) IsZero() bool {
	return r == Range{}
}

func (r Range) Overlaps(o Range) bool {
	return r.Lo <= o.Hi && o.Lo <= r.Hi
}

func (r Range) String() string {
	if r.Lo == r.Hi {
		return strconv.FormatUint(uint64(r.Lo), 10)
	}
	return fmt.Sprintf("%d-%d", r.Lo, r.Hi)
}
