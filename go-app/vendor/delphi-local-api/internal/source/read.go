package source

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// ReadBounded reads one no-follow regular file within the caller's byte bound.
func ReadBounded(path string, limit int64) ([]byte, error) {
	if limit < 0 {
		return nil, ErrLimit
	}
	if err := RejectSymlinkComponents(path); err != nil {
		return nil, err
	}
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Size() < 0 || before.Size() > limit {
		return nil, ErrUnsafePath
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) || !opened.Mode().IsRegular() {
		return nil, errors.New("file changed before bounded read")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, ErrLimit
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || int64(len(data)) != after.Size() {
		return nil, errors.New("file changed during bounded read")
	}
	return data, nil
}

// ContainedPath joins a workspace root and a slash-relative path after
// rejecting lexical traversal and existing symlink components.
func ContainedPath(root, relative string) (string, error) {
	if err := ValidateRelativePath(relative); err != nil {
		return "", ErrUnsafePath
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	path := filepath.Join(absoluteRoot, filepath.FromSlash(relative))
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(absoluteRoot, absolutePath)
	if err != nil || rel == ".." || filepath.IsAbs(rel) || len(rel) >= 3 && rel[:3] == ".."+string(os.PathSeparator) {
		return "", ErrUnsafePath
	}
	if err := RejectSymlinkComponents(absolutePath); err != nil {
		return "", err
	}
	return absolutePath, nil
}
