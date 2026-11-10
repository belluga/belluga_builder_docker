package source

import (
	"errors"
	"os"
	"strings"
	"unicode/utf8"
)

const (
	MaxPathBytes      = 1024
	MaxFileBytes      = 2 << 20
	MaxFiles          = 2048
	MaxInventoryNodes = MaxFiles*(MaxPathBytes/2+1) + 1
)

type FileMode uint8

const (
	ModeRegular FileMode = iota + 1
	ModeDirectory
	ModeSymlink
	ModeSubmodule
	ModeSpecial
)

type Entry struct {
	Path string
	Mode FileMode
	Size int64
}

// Tree supplies bounded, stable reads from one explicitly selected source.
type Source interface {
	Mode() string
	Revision() *string
	List(prefix string) ([]Entry, error)
	Read(path string, limit int64) ([]byte, FileMode, error)
	CheckRegular(path string, limit int64) error
	VerifyStable() error
}

func ValidateRelativePath(path string) error {
	if !utf8.ValidString(path) || path == "" || strings.HasPrefix(path, "/") || strings.ContainsAny(path, "\\\x00") {
		return errors.New("path is not a valid relative UTF-8 slash path")
	}
	for _, r := range path {
		if r < 0x20 || r == 0x7f {
			return errors.New("path contains a control character")
		}
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "" || segment == "." || segment == ".." || segment == ".git" {
			return errors.New("path contains an empty, dot, dot-dot, or reserved .git segment")
		}
	}
	return nil
}

func hasGitSegment(path string) bool {
	for _, segment := range strings.Split(path, "/") {
		if segment == ".git" {
			return true
		}
	}
	return false
}

var (
	ErrUnsafePath          = errors.New("unsafe path or filesystem node")
	ErrLimit               = errors.New("bounded reader limit exceeded")
	ErrRevisionUnavailable = errors.New("committed revision unavailable")
	ErrNotExist            = os.ErrNotExist
)
