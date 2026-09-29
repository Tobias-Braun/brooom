package session

import (
	"crypto/rand"
	"encoding/hex"
)

func randomSuffix() string {
	b := make([]byte, 2)
	// crypto/rand.Read never returns an error on supported platforms.
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
