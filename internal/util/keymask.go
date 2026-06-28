package util

// MaskKey masks an API key for safe logging, showing only first 4 and last 4 characters.
func MaskKey(key string) string {
	if len(key) <= 8 {
		return "***"
	}
	return key[:4] + "***" + key[len(key)-4:]
}