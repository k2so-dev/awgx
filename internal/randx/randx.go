package randx

import (
	"crypto/rand"
	"math/big"
)

func Between(lo, hi int) int {
	if hi <= lo {
		return lo
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(hi-lo+1)))
	if err != nil {
		panic(err)
	}
	return lo + int(n.Int64())
}
