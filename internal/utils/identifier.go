package utils

import (
	"crypto/rand"
	"math/big"
)

var (
	sourceLetters = []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ")
)

func GenerateRandomStringID(length int) string {
	b := make([]rune, length)
	for i := range b {
		idx, _ := rand.Int(rand.Reader, big.NewInt(int64(len(sourceLetters))))
		b[i] = sourceLetters[idx.Int64()]
	}

	return string(b)
}
