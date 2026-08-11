package remote

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
)

const maxServerTokenSize = 64 << 10

// LoadServerTokenFile loads a global server token from a regular, non-symlink
// file. Docker secret modes such as 0444 are accepted, but group or other
// write access is rejected.
func LoadServerTokenFile(path string) (string, error) {
	if path == "" {
		return "", errors.New("remote: empty server token file path")
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", fmt.Errorf("remote: open server token file: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("remote: stat server token file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("remote: server token file is not a regular file")
	}
	if info.Mode().Perm()&0022 != 0 {
		return "", fmt.Errorf("remote: server token file is group or other writable: %04o", info.Mode().Perm())
	}
	if info.Size() > maxServerTokenSize {
		return "", fmt.Errorf("remote: server token file exceeds %d bytes", maxServerTokenSize)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxServerTokenSize+1))
	if err != nil {
		return "", fmt.Errorf("remote: read server token file: %w", err)
	}
	if len(data) > maxServerTokenSize {
		return "", fmt.Errorf("remote: server token file exceeds %d bytes", maxServerTokenSize)
	}
	data = bytes.TrimRight(data, "\r\n")
	if len(data) == 0 {
		return "", errors.New("remote: server token is empty")
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return "", errors.New("remote: server token contains a NUL byte")
	}
	return string(data), nil
}
