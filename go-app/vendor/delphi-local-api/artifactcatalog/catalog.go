// Package artifactcatalog exposes PACED's canonical Prototype v3 evaluator
// to consumers outside the local-api internal tree. Validation remains owned
// by internal/artifact_catalog; this package only re-exports its public types
// and entry points.
package artifactcatalog

import (
	canonical "delphi-local-api/internal/artifact_catalog"
	knowledge "delphi-local-api/internal/knowledge_status"
)

const (
	SchemaVersion          = canonical.SchemaVersion
	PrototypeSchemaVersion = canonical.PrototypeSchemaVersion
	MaxPrototypes          = canonical.MaxPrototypes
	MaxScreens             = canonical.MaxScreens
	MaxStates              = canonical.MaxStates
	MaxFiles               = canonical.MaxFiles
	MaxFileBytes           = canonical.MaxFileBytes
	MaxTotalBytes          = canonical.MaxTotalBytes
	MaxManifestBytes       = canonical.MaxManifestBytes
	MaxPathBytes           = canonical.MaxPathBytes
	MaxTransitions         = canonical.MaxTransitions
	MaxScenarios           = canonical.MaxScenarios
	MaxScenarioSteps       = canonical.MaxScenarioSteps
	MaxRelated             = canonical.MaxRelated
	MaxApprovedReferences  = canonical.MaxApprovedReferences
	MaxInventoryNodes      = canonical.MaxInventoryNodes
	AuthorityScope         = canonical.AuthorityScope
	TargetPrototypes       = canonical.TargetPrototypes
	DesignSystemPending    = canonical.DesignSystemPending
	ModeRegular            = canonical.ModeRegular
	ModeDirectory          = canonical.ModeDirectory
	ModeSymlink            = canonical.ModeSymlink
	ModeSubmodule          = canonical.ModeSubmodule
	ModeSpecial            = canonical.ModeSpecial
)

type (
	FileMode             = canonical.FileMode
	Entry                = canonical.Entry
	Source               = canonical.Source
	WorkingTree          = canonical.WorkingTree
	GitTree              = canonical.GitTree
	Catalog              = canonical.Catalog
	Prototype            = canonical.Prototype
	Manifest             = canonical.Manifest
	Screen               = canonical.Screen
	State                = canonical.State
	ApprovedReference    = canonical.ApprovedReference
	Transition           = canonical.Transition
	Scenario             = canonical.Scenario
	ScenarioStep         = canonical.ScenarioStep
	Related              = canonical.Related
	DesignSystemRef      = canonical.DesignSystemRef
	Diagnostic           = canonical.Diagnostic
	Item                 = canonical.Item
	Response             = canonical.Response
	DigestRecord         = canonical.DigestRecord
	PathFailure          = canonical.PathFailure
	CanonicalManifest    = knowledge.CanonicalManifest
	CanonicalReference   = knowledge.CanonicalReference
)

var (
	ErrUnsafePath          = canonical.ErrUnsafePath
	ErrLimit               = canonical.ErrLimit
	ErrRevisionUnavailable = canonical.ErrRevisionUnavailable
)

func NewWorkingTree(root string) (*WorkingTree, error) {
	return canonical.NewWorkingTree(root)
}

func NewGitTree(root, revision string) (*GitTree, error) {
	return canonical.NewGitTree(root, revision)
}

func Evaluate(source Source, projectID string) Response {
	return canonical.Evaluate(source, projectID)
}

func EvaluateWithKnowledge(source Source, projectID string, selection *CanonicalManifest) Response {
	return canonical.EvaluateWithKnowledge(source, projectID, selection)
}

func ValidateRelativePath(path string) error { return canonical.ValidateRelativePath(path) }

func DecodeStrict(data []byte, target any) error { return canonical.DecodeStrict(data, target) }

func InventoryDigest(sourceID string, records []DigestRecord) (string, error) {
	return canonical.InventoryDigest(sourceID, records)
}

func RejectSymlinkComponents(path string) error { return canonical.RejectSymlinkComponents(path) }

func EnsureSafeDirectory(path string) error { return canonical.EnsureSafeDirectory(path) }
