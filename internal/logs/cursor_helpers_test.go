package logs

import (
	"encoding/base64"
	"fmt"
	"strings"
)

func encodeRaw(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

func manyEntries(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = fmt.Sprintf("%d:1", i+1)
	}
	return strings.Join(parts, ",")
}
