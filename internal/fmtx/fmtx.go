package fmtx

import (
	"fmt"
	"time"
)

func Ago(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t).Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

func Bytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func OnOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

func Rate(mbit int) string {
	if mbit == 0 {
		return "off"
	}
	return fmt.Sprintf("%d Mbit/s", mbit)
}

func Port(pinned, current int) string {
	switch {
	case pinned != 0:
		return fmt.Sprint(pinned)
	case current != 0:
		return fmt.Sprintf("auto (%d)", current)
	}
	return "auto"
}
