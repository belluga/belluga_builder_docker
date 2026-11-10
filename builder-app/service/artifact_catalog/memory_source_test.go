package artifact_catalog

import (
	"errors"
	"strings"
)

type memorySource struct {
	files           map[string][]byte
	modes           map[string]FileMode
	entries         []Entry
	mutateAfterRead string
	changed         bool
	listErr         error
	readCalls       int
	readPaths       map[string]int
	checkPaths      map[string]int
}

func (s *memorySource) Mode() string      { return "working-tree" }
func (s *memorySource) Revision() *string { return nil }
func (s *memorySource) List(prefix string) ([]Entry, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	result := make([]Entry, 0, len(s.entries))
	for _, entry := range s.entries {
		if entry.Path == prefix || strings.HasPrefix(entry.Path, prefix+"/") {
			result = append(result, entry)
		}
	}
	if len(result) == 0 {
		return nil, ErrNotExist
	}
	return result, nil
}
func (s *memorySource) Read(path string, limit int64) ([]byte, FileMode, error) {
	s.readCalls++
	if s.readPaths == nil {
		s.readPaths = map[string]int{}
	}
	s.readPaths[path]++
	if mode, ok := s.modes[path]; ok {
		return nil, mode, errors.New("unsafe node")
	}
	data, ok := s.files[path]
	if !ok {
		return nil, ModeSpecial, ErrNotExist
	}
	if int64(len(data)) > limit {
		return nil, ModeRegular, ErrLimit
	}
	read := append([]byte(nil), data...)
	if path == s.mutateAfterRead && !s.changed {
		s.files[path] = append(append([]byte(nil), data...), []byte(" changed after read")...)
		s.changed = true
	}
	return read, ModeRegular, nil
}
func (s *memorySource) CheckRegular(path string, limit int64) error {
	if s.checkPaths == nil {
		s.checkPaths = map[string]int{}
	}
	s.checkPaths[path]++
	if mode, ok := s.modes[path]; ok {
		if mode != ModeRegular {
			return ErrUnsafePath
		}
	}
	data, ok := s.files[path]
	if !ok {
		return ErrNotExist
	}
	if int64(len(data)) > limit {
		return ErrLimit
	}
	return nil
}
func (s *memorySource) VerifyStable() error {
	if s.changed {
		return errors.New("fixture mutation detected")
	}
	return nil
}
