package platform

import (
	"errors"
	"io"
	"os"
)

var ErrLocked = errors.New("file lock already held")

type FileLock struct {
	Path string
	file *os.File
}

func (l *FileLock) Unlock() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := unlockFile(l.file)
	closeErr := l.file.Close()
	l.file = nil
	if err != nil {
		return err
	}
	return closeErr
}

func (l *FileLock) WriteString(s string) error {
	if l == nil || l.file == nil {
		return os.ErrInvalid
	}
	if err := l.file.Truncate(0); err != nil {
		return err
	}
	if _, err := l.file.Seek(0, 0); err != nil {
		return err
	}
	n, err := l.file.WriteString(s)
	if err != nil {
		return err
	}
	if n != len(s) {
		return io.ErrShortWrite
	}
	return nil
}
