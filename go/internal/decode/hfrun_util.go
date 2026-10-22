package decode

import "os"

// createTempWAV makes the temp file for file-based decoder paths.
func createTempWAV() (*os.File, error) {
	return os.CreateTemp("", "sdrstream-hf-*.wav")
}
