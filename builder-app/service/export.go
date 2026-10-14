package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type bundleLock struct {
	Commit      string         `json:"commit"`
	Fingerprint string         `json:"fingerprint"`
	Files       []approvedFile `json:"files"`
}

func exportCommit(repo, commit, output string) error {
	if commit != approvedCommit {
		return errors.New("commit is not approved")
	}
	resolved, err := gitOutput(repo, "rev-parse", "--verify", commit+"^{commit}")
	if err != nil || strings.TrimSpace(string(resolved)) != commit {
		return errors.New("approved commit is unavailable")
	}
	if _, err := os.Stat(output); err == nil {
		if _, validationErr := loadBundle(output); validationErr != nil {
			return errors.New("existing bundle is invalid; refusing replacement")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("cannot inspect existing bundle destination")
	}
	// Build in a sibling directory, then publish the complete bundle in one rename.
	parent := filepath.Dir(output)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return errors.New("cannot prepare bundle destination")
	}
	tmp, err := os.MkdirTemp(parent, ".builder-bundle-*")
	if err != nil {
		return errors.New("cannot prepare bundle")
	}
	defer os.RemoveAll(tmp)
	for _, file := range approvedFiles {
		sizeOutput, sizeErr := gitOutput(repo, "cat-file", "-s", commit+":"+file.Path)
		if sizeErr != nil {
			return errors.New("approved bundle source is incomplete")
		}
		var size int64
		if _, err := fmt.Sscanf(strings.TrimSpace(string(sizeOutput)), "%d", &size); err != nil || size < 0 || size > maxDocumentBytes {
			return errors.New("approved bundle file exceeds size limit")
		}
		content, err := gitOutput(repo, "show", commit+":"+file.Path)
		if err != nil {
			return errors.New("approved bundle source is incomplete")
		}
		digest := sha256.Sum256(content)
		if hex.EncodeToString(digest[:]) != file.Digest {
			return errors.New("approved source digest mismatch")
		}
		destination := filepath.Join(tmp, "foundation", filepath.FromSlash(file.Path))
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return errors.New("cannot prepare bundle files")
		}
		if err := os.WriteFile(destination, content, 0o444); err != nil {
			return errors.New("cannot write bundle file")
		}
	}
	lock := bundleLock{Commit: commit, Fingerprint: fingerprint(approvedFiles), Files: approvedFiles}
	if lock.Fingerprint != approvedFingerprint {
		return errors.New("approved inventory fingerprint mismatch")
	}
	encoded, err := json.Marshal(lock)
	if err != nil {
		return errors.New("cannot encode bundle identity")
	}
	if err := os.WriteFile(filepath.Join(tmp, "bundle.json"), encoded, 0o444); err != nil {
		return errors.New("cannot write bundle identity")
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return errors.New("cannot make bundle readable by runtime")
	}
	if _, err := os.Stat(output); err == nil {
		return errors.New("bundle destination appeared during export")
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("cannot inspect bundle destination")
	}
	if err := os.Rename(tmp, output); err != nil {
		return errors.New("cannot publish complete bundle")
	}
	return nil
}

func gitOutput(repo string, args ...string) ([]byte, error) {
	gitArgs := append([]string{"-c", "safe.directory=" + repo, "-C", repo}, args...)
	cmd := exec.Command("git", gitArgs...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git operation failed: %w", err)
	}
	return output, nil
}
