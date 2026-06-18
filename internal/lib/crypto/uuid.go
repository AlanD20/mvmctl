package crypto

import (
	"crypto/rand"
	"fmt"
)

// UUIDV4 generates a random UUID v4 string.
func UUIDV4() string {
	u := make([]byte, 16)
	rand.Read(u)
	u[6] = (u[6] & 0x0f) | 0x40
	u[8] = (u[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		u[0:4], u[4:6], u[6:8], u[8:10], u[10:16])
}
