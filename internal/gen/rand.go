package gen

import "github.com/k2so-dev/awgx/internal/randx"

func shuffle[T any](s []T) {
	for i := len(s) - 1; i > 0; i-- {
		j := randx.Between(0, i)
		s[i], s[j] = s[j], s[i]
	}
}
