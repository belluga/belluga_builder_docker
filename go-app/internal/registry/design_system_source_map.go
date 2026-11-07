package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	ac "belluga-builder-viewer/artifact_catalog"
)

type fileDesignSystemSourceMapProvider struct{ root string }

func (p fileDesignSystemSourceMapProvider) Read(ctx context.Context, projectID, companyID string, binding Binding) (ac.SourceBindings, error) {
	if err := ctx.Err(); err != nil {
		return ac.SourceBindings{}, err
	}
	if p.root == "" || binding.SourceBindingsStatePath == "" || !validFingerprint(binding.SourceBindingsDigest) ||
		ac.ValidateRelativePath(binding.SourceBindingsStatePath) != nil || strings.Contains(binding.SourceBindingsStatePath, "\\") {
		return ac.SourceBindings{}, ErrUnavailable
	}
	rootInfo, err := os.Lstat(p.root)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return ac.SourceBindings{}, ErrUnavailable
	}
	root, err := filepath.Abs(p.root)
	if err != nil {
		return ac.SourceBindings{}, ErrUnavailable
	}
	path := filepath.Join(root, filepath.FromSlash(binding.SourceBindingsStatePath))
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || relative == ".." {
		return ac.SourceBindings{}, ErrUnavailable
	}
	current := root
	parts := strings.Split(relative, string(filepath.Separator))
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 || i < len(parts)-1 && !info.IsDir() || i == len(parts)-1 && !info.Mode().IsRegular() {
			return ac.SourceBindings{}, ErrUnavailable
		}
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return ac.SourceBindings{}, ErrUnavailable
	}
	file := os.NewFile(uintptr(fd), "source-map")
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, ac.MaxSourceBindingsBytes+1))
	if err != nil || len(data) == 0 || len(data) > ac.MaxSourceBindingsBytes {
		return ac.SourceBindings{}, ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return ac.SourceBindings{}, err
	}
	sum := sha256.Sum256(data)
	if binding.SourceBindingsDigest != "sha256:"+hex.EncodeToString(sum[:]) {
		return ac.SourceBindings{}, ErrUnavailable
	}
	bindings, err := ac.DecodeSourceBindings(data)
	if err != nil || bindings.ProjectID != projectID || bindings.CompanyID != companyID {
		return ac.SourceBindings{}, ErrUnavailable
	}
	return bindings, nil
}
