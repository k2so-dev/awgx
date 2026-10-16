package core

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

const MaxDays = 36500

func ParseDuration(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	if n, ok := strings.CutSuffix(s, "d"); ok {
		days, err := strconv.Atoi(n)
		if err != nil || days <= 0 || days > MaxDays {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 || d > MaxDays*24*time.Hour {
		return 0, fmt.Errorf("invalid duration %q", s)
	}
	return d, nil
}
