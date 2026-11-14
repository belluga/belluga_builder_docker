package source

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

type WorkingTree struct {
	root     string
	prefix   string
	mu       sync.Mutex
	cache    map[string][]byte
	baseline map[string]fileStamp
	reads    map[string]fileStamp
}

type fileStamp struct {
	info   os.FileInfo
	mode   FileMode
	change string
}

type PathFailure struct {
	Path string
	Err  error
}

func (failure *PathFailure) Error() string { return "cannot inspect Foundation path " + failure.Path }
func (failure *PathFailure) Unwrap() error { return failure.Err }

func NewWorkingTree(root string) (*WorkingTree, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := rejectSymlinkComponents(absolute); err != nil {
		return nil, err
	}
	if err := requireRepositoryRoot(absolute); err != nil {
		return nil, err
	}
	return &WorkingTree{root: absolute, cache: make(map[string][]byte), reads: make(map[string]fileStamp)}, nil
}

func (s *WorkingTree) Mode() string      { return "working-tree" }
func (s *WorkingTree) Revision() *string { return nil }

func (s *WorkingTree) List(prefix string) ([]Entry, error) {
	if err := ValidateRelativePath(prefix); err != nil {
		return nil, err
	}
	root, err := s.safePath(prefix)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && prefix == "prototypes" {
			return nil, ErrNotExist
		}
		return nil, err
	}
	info, err := os.Lstat(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && prefix == "prototypes" {
			return nil, ErrNotExist
		}
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%w: collection root is not a directory", ErrUnsafePath)
	}
	entries := []Entry{{Path: prefix, Mode: ModeDirectory}}
	observed := map[string]fileStamp{prefix: stampFor(info)}
	err = filepath.WalkDir(root, func(path string, item fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			rel, relErr := filepath.Rel(s.root, path)
			if relErr != nil {
				return errors.New("collection inventory could not be inspected")
			}
			return &PathFailure{Path: filepath.ToSlash(rel), Err: walkErr}
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(s.root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if err := ValidateRelativePath(rel); err != nil {
			return &PathFailure{Path: rel, Err: fmt.Errorf("%w: invalid or reserved inventory path", ErrUnsafePath)}
		}
		if len(entries) >= MaxInventoryNodes {
			return ErrLimit
		}
		if hasGitSegment(rel) {
			entries = append(entries, Entry{Path: rel, Mode: ModeSubmodule})
			if item.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := os.Lstat(path)
		if err != nil {
			return &PathFailure{Path: rel, Err: err}
		}
		entries = append(entries, Entry{Path: rel, Mode: fileMode(info.Mode()), Size: info.Size()})
		observed[rel] = stampFor(info)
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return nil
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.baseline == nil {
		s.prefix = prefix
		s.baseline = observed
	} else if s.prefix != prefix || !sameInventory(s.baseline, observed) {
		s.mu.Unlock()
		return nil, errors.New("working-tree inventory changed during evaluation")
	}
	s.mu.Unlock()
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

func (s *WorkingTree) Read(path string, limit int64) ([]byte, FileMode, error) {
	if err := ValidateRelativePath(path); err != nil {
		return nil, ModeSpecial, err
	}
	s.mu.Lock()
	if data, ok := s.cache[path]; ok {
		if int64(len(data)) > limit {
			s.mu.Unlock()
			return nil, ModeRegular, ErrLimit
		}
		copyOfData := append([]byte(nil), data...)
		s.mu.Unlock()
		return copyOfData, ModeRegular, nil
	}
	s.mu.Unlock()
	absolute, err := s.safePath(path)
	if err != nil {
		return nil, ModeSpecial, err
	}
	before, err := os.Lstat(absolute)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ModeSpecial, ErrNotExist
		}
		return nil, ModeSpecial, err
	}
	mode := fileMode(before.Mode())
	if mode != ModeRegular {
		return nil, mode, fmt.Errorf("%w: requested path is not a regular file", ErrUnsafePath)
	}
	if before.Size() > limit {
		return nil, mode, ErrLimit
	}
	fd, err := os.OpenFile(absolute, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, mode, err
	}
	opened, err := fd.Stat()
	if err != nil || !os.SameFile(before, opened) {
		_ = fd.Close()
		return nil, mode, errors.New("file changed before it was opened")
	}
	data, err := io.ReadAll(io.LimitReader(fd, limit+1))
	closeErr := fd.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, mode, err
	}
	if int64(len(data)) > limit {
		return nil, mode, ErrLimit
	}
	after, err := os.Lstat(absolute)
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) || int64(len(data)) != after.Size() {
		return nil, mode, errors.New("file changed while it was being evaluated")
	}
	s.mu.Lock()
	s.cache[path] = append([]byte(nil), data...)
	s.reads[path] = stampFor(after)
	s.mu.Unlock()
	return data, mode, nil
}

// CheckRegular validates a reference at this working-tree observation without
// retaining or reading its payload. VerifyStable rechecks the observed file.
func (s *WorkingTree) CheckRegular(path string, limit int64) error {
	absolute, err := s.safePath(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrNotExist
		}
		return err
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrNotExist
		}
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: referenced path is not a regular file", ErrUnsafePath)
	}
	if info.Size() > limit {
		return ErrLimit
	}
	s.mu.Lock()
	s.reads[path] = stampFor(info)
	s.mu.Unlock()
	return nil
}

func (s *WorkingTree) VerifyStable() error {
	s.mu.Lock()
	prefix := s.prefix
	s.mu.Unlock()
	if prefix == "" {
		return errors.New("managed inventory was not captured")
	}
	if _, err := s.List(prefix); err != nil {
		return err
	}
	s.mu.Lock()
	reads := make(map[string]fileStamp, len(s.reads))
	for path, stamp := range s.reads {
		reads[path] = stamp
	}
	s.mu.Unlock()
	for path, expected := range reads {
		absolute, err := s.safePath(path)
		if err != nil {
			return errors.New("a read file or ancestor changed during evaluation")
		}
		current, err := os.Lstat(absolute)
		if err != nil {
			return errors.New("a read file changed during evaluation")
		}
		if !sameStamp(expected, stampFor(current)) {
			return errors.New("a read file changed during evaluation")
		}
	}
	return nil
}

func sameInventory(left, right map[string]fileStamp) bool {
	if len(left) != len(right) {
		return false
	}
	for path, expected := range left {
		current, ok := right[path]
		if !ok || !sameStamp(expected, current) {
			return false
		}
	}
	return true
}

func sameStamp(left, right fileStamp) bool {
	return left.mode == right.mode && left.info != nil && right.info != nil && os.SameFile(left.info, right.info) && left.info.Size() == right.info.Size() && left.info.Mode() == right.info.Mode() && left.info.ModTime().Equal(right.info.ModTime()) && left.change == right.change
}

func stampFor(info os.FileInfo) fileStamp {
	stamp := fileStamp{info: info, mode: fileMode(info.Mode())}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		stamp.change = fmt.Sprintf("%d:%d:%d:%d", stat.Dev, stat.Ino, stat.Ctim.Sec, stat.Ctim.Nsec)
	}
	return stamp
}

func (s *WorkingTree) safePath(rel string) (string, error) {
	if err := ValidateRelativePath(rel); err != nil {
		return "", err
	}
	current := s.root
	parts := strings.Split(rel, "/")
	for index, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return current, &PathFailure{Path: strings.Join(parts[:index+1], "/"), Err: err}
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("%w: symbolic-link path component", ErrUnsafePath)
		}
		if index < len(parts)-1 && !info.IsDir() {
			return "", fmt.Errorf("%w: non-directory path ancestor", ErrUnsafePath)
		}
	}
	return current, nil
}

type GitTree struct {
	root     string
	revision string
	mu       sync.Mutex
	cache    map[string]gitObject
}

type gitObject struct {
	data []byte
	mode FileMode
}

func NewGitTree(root, revision string) (*GitTree, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := rejectSymlinkComponents(absolute); err != nil {
		return nil, err
	}
	if err := requireRepositoryRoot(absolute); err != nil {
		return nil, err
	}
	if revision == "" {
		revision = "HEAD"
	}
	resolved, err := gitRun(absolute, "rev-parse", "--verify", revision+"^{commit}")
	if err != nil {
		return nil, ErrRevisionUnavailable
	}
	resolved = strings.TrimSpace(resolved)
	if revision != "HEAD" && revision != resolved {
		return nil, errors.New("revision must be a full commit SHA")
	}
	return &GitTree{root: absolute, revision: resolved, cache: make(map[string]gitObject)}, nil
}

func (s *GitTree) Mode() string        { return "committed" }
func (s *GitTree) Revision() *string   { return &s.revision }
func (s *GitTree) VerifyStable() error { return nil }

func (s *GitTree) List(prefix string) ([]Entry, error) {
	if err := ValidateRelativePath(prefix); err != nil {
		return nil, err
	}
	const maxTreeOutputBytes = int64(MaxFiles+1) * (2*MaxPathBytes + 128)
	out, err := gitRunBytesLimit(s.root, maxTreeOutputBytes, "ls-tree", "-r", "-l", "-z", "--full-tree", s.revision, "--", ":(literal)"+prefix)
	if err != nil {
		return nil, err
	}
	entries := []Entry{}
	seenDirs := map[string]bool{}
	for _, record := range bytes.Split(out, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		tab := bytes.IndexByte(record, '\t')
		if tab < 0 {
			return nil, errors.New("malformed Git tree response")
		}
		metadata, pathBytes := string(record[:tab]), record[tab+1:]
		fields := strings.Fields(metadata)
		if len(fields) != 4 {
			return nil, errors.New("malformed Git tree metadata")
		}
		path := string(pathBytes)
		mode := gitMode(fields[0], fields[1])
		size := int64(0)
		if fields[3] != "-" {
			size, err = strconv.ParseInt(fields[3], 10, 64)
			if err != nil || size < 0 {
				return nil, errors.New("malformed Git blob size")
			}
		}
		if err := ValidateRelativePath(path); err != nil {
			return nil, err
		}
		entries = append(entries, Entry{Path: path, Mode: mode, Size: size})
		for parent := filepath.ToSlash(filepath.Dir(path)); parent != "."; parent = filepath.ToSlash(filepath.Dir(parent)) {
			if parent != prefix && !strings.HasPrefix(parent, prefix+"/") {
				break
			}
			if !seenDirs[parent] {
				seenDirs[parent] = true
				entries = append(entries, Entry{Path: parent, Mode: ModeDirectory})
			}
			if parent == prefix {
				break
			}
		}
	}
	if len(entries) > MaxInventoryNodes {
		return nil, ErrLimit
	}
	if !seenDirs[prefix] {
		for _, entry := range entries {
			if entry.Path == prefix && entry.Mode != ModeDirectory {
				return []Entry{entry}, nil
			}
		}
		// `ls-tree -r` does not reliably include a symlink at the requested
		// collection root. Inspect that single tree entry explicitly so callers
		// see the symlink as an unsafe node rather than misclassifying it as absent.
		direct, directErr := gitRunBytesLimit(s.root, int64(2*MaxPathBytes+256), "ls-tree", "-l", "-z", "--full-tree", s.revision, "--", ":(literal)"+prefix)
		if directErr == nil {
			for _, record := range bytes.Split(direct, []byte{0}) {
				if len(record) == 0 {
					continue
				}
				tab := bytes.IndexByte(record, '\t')
				if tab < 0 || string(record[tab+1:]) != prefix {
					continue
				}
				fields := strings.Fields(string(record[:tab]))
				if len(fields) != 4 {
					return nil, errors.New("malformed Git tree metadata")
				}
				if gitMode(fields[0], fields[1]) != ModeDirectory {
					return []Entry{{Path: prefix, Mode: gitMode(fields[0], fields[1])}}, nil
				}
			}
		}
		if prefix == "prototypes" {
			return nil, ErrNotExist
		}
		return nil, &PathFailure{Path: prefix, Err: ErrNotExist}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

func (s *GitTree) Read(path string, limit int64) ([]byte, FileMode, error) {
	if err := ValidateRelativePath(path); err != nil {
		return nil, ModeSpecial, err
	}
	s.mu.Lock()
	cached, ok := s.cache[path]
	if ok {
		if int64(len(cached.data)) > limit {
			s.mu.Unlock()
			return nil, cached.mode, ErrLimit
		}
		data := append([]byte(nil), cached.data...)
		mode := cached.mode
		s.mu.Unlock()
		return data, mode, nil
	}
	s.mu.Unlock()
	out, err := gitRunBytesLimit(s.root, int64(2*MaxPathBytes+256), "ls-tree", "-l", "-z", s.revision, "--", ":(literal)"+path)
	if err != nil {
		return nil, ModeSpecial, err
	}
	var object string
	var mode FileMode
	found := false
	for _, record := range bytes.Split(out, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		tab := bytes.IndexByte(record, '\t')
		if tab < 0 {
			continue
		}
		if string(record[tab+1:]) != path {
			continue
		}
		fields := strings.Fields(string(record[:tab]))
		if len(fields) != 4 {
			return nil, ModeSpecial, errors.New("malformed Git tree metadata")
		}
		object = fields[2]
		mode = gitMode(fields[0], fields[1])
		if fields[3] == "-" {
			return nil, mode, fmt.Errorf("%w: requested Git object is not a blob", ErrUnsafePath)
		}
		objectSize, sizeErr := strconv.ParseInt(fields[3], 10, 64)
		if sizeErr != nil || objectSize < 0 {
			return nil, mode, errors.New("malformed Git blob size")
		}
		if objectSize > limit {
			return nil, mode, ErrLimit
		}
		found = true
		break
	}
	if !found {
		return nil, ModeSpecial, ErrNotExist
	}
	if mode != ModeRegular {
		return nil, mode, fmt.Errorf("%w: committed path is not a regular blob", ErrUnsafePath)
	}
	data, err := gitRunBytesLimit(s.root, limit, "cat-file", "blob", object)
	if err != nil {
		return nil, mode, err
	}
	if int64(len(data)) > limit {
		return nil, mode, ErrLimit
	}
	s.mu.Lock()
	s.cache[path] = gitObject{data: append([]byte(nil), data...), mode: mode}
	s.mu.Unlock()
	return data, mode, nil
}

// CheckRegular inspects the selected tree entry and blob size without reading
// or retaining related evidence contents.
func (s *GitTree) CheckRegular(path string, limit int64) error {
	if err := ValidateRelativePath(path); err != nil || hasGitSegment(path) {
		return ErrUnsafePath
	}
	out, err := gitRunBytesLimit(s.root, int64(2*MaxPathBytes+256), "ls-tree", "-l", "-z", s.revision, "--", ":(literal)"+path)
	if err != nil {
		return err
	}
	for _, record := range bytes.Split(out, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		tab := bytes.IndexByte(record, '\t')
		if tab < 0 || string(record[tab+1:]) != path {
			continue
		}
		fields := strings.Fields(string(record[:tab]))
		if len(fields) != 4 {
			return errors.New("malformed Git tree metadata")
		}
		mode := gitMode(fields[0], fields[1])
		if fields[3] == "-" || mode != ModeRegular {
			return fmt.Errorf("%w: referenced Git object is not a regular blob", ErrUnsafePath)
		}
		size, parseErr := strconv.ParseInt(fields[3], 10, 64)
		if parseErr != nil || size < 0 {
			return errors.New("malformed Git blob size")
		}
		if size > limit {
			return ErrLimit
		}
		return nil
	}
	return ErrNotExist
}

func gitMode(mode, kind string) FileMode {
	switch {
	case mode == "040000" || kind == "tree":
		return ModeDirectory
	case mode == "120000":
		return ModeSymlink
	case mode == "160000" || kind == "commit":
		return ModeSubmodule
	case strings.HasPrefix(mode, "100"):
		return ModeRegular
	default:
		return ModeSpecial
	}
}

func gitRun(root string, args ...string) (string, error) {
	out, err := gitRunBytes(root, args...)
	return string(out), err
}
func gitRunBytes(root string, args ...string) ([]byte, error) {
	return gitRunBytesLimit(root, 4096, args...)
}

func gitRunBytesLimit(root string, limit int64, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"--no-replace-objects", "-C", root}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, errors.New("Git operation could not be started")
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, errors.New("Git operation could not be started")
	}
	out, readErr := io.ReadAll(io.LimitReader(stdout, limit+1))
	if readErr != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, errors.New("Git output could not be read")
	}
	if int64(len(out)) > limit {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, ErrLimit
	}
	if err := cmd.Wait(); err != nil {
		return nil, errors.New("Git operation failed")
	}
	return out, nil
}

func rejectSymlinkComponents(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	volume := filepath.VolumeName(absolute)
	rest := strings.TrimPrefix(absolute, volume)
	current := volume + string(os.PathSeparator)
	for _, part := range strings.Split(strings.TrimPrefix(rest, string(os.PathSeparator)), string(os.PathSeparator)) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: root path includes a symbolic link", ErrUnsafePath)
		}
	}
	return nil
}

func requireRepositoryRoot(path string) error {
	top, err := gitRun(path, "rev-parse", "--show-toplevel")
	if err != nil {
		return errors.New("Foundation root must be its own Git repository")
	}
	top, err = filepath.Abs(strings.TrimSpace(top))
	if err != nil || filepath.Clean(top) != filepath.Clean(path) {
		return errors.New("Foundation root must resolve to the bound Git repository root")
	}
	return nil
}

// RequireRepositoryRoot accepts only the exact top-level of the selected Git checkout.
func RequireRepositoryRoot(path string) error { return requireRepositoryRoot(path) }

// RejectSymlinkComponents validates every existing component of an absolute root.
func RejectSymlinkComponents(path string) error { return rejectSymlinkComponents(path) }

// EnsureSafeDirectory creates missing output directories one component at a time.
func EnsureSafeDirectory(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	volume := filepath.VolumeName(absolute)
	rest := strings.TrimPrefix(absolute, volume)
	current := volume + string(os.PathSeparator)
	for _, part := range strings.Split(strings.TrimPrefix(rest, string(os.PathSeparator)), string(os.PathSeparator)) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			info, err = os.Lstat(current)
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("%w: output parent is not a real directory", ErrUnsafePath)
		}
	}
	return nil
}

func fileMode(mode os.FileMode) FileMode {
	switch {
	case mode.IsDir():
		return ModeDirectory
	case mode&os.ModeSymlink != 0:
		return ModeSymlink
	case mode.IsRegular():
		return ModeRegular
	default:
		return ModeSpecial
	}
}
