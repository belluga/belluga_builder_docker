package artifact_catalog

import "delphi-local-api/internal/source"

type WorkingTree = source.WorkingTree
type GitTree = source.GitTree
type PathFailure = source.PathFailure

var (
	ErrUnsafePath          = source.ErrUnsafePath
	ErrLimit               = source.ErrLimit
	ErrRevisionUnavailable = source.ErrRevisionUnavailable
)

func NewWorkingTree(root string) (*WorkingTree, error)   { return source.NewWorkingTree(root) }
func NewGitTree(root, revision string) (*GitTree, error) { return source.NewGitTree(root, revision) }
func RejectSymlinkComponents(path string) error          { return source.RejectSymlinkComponents(path) }
func EnsureSafeDirectory(path string) error              { return source.EnsureSafeDirectory(path) }
