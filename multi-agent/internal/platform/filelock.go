package platform

import (
	"errors"
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
