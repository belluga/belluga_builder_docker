package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
)

const approvedCommit = "cfe9a6e45ea3cc593c43ab7e2bd20ad04722d6fb"

type approvedFile struct {
	ID     string `json:"id,omitempty"`
	Path   string `json:"path"`
	Digest string `json:"sha256"`
}

var approvedFiles = []approvedFile{
	{ID: "landing", Path: "project_landing.md", Digest: "6488b75ac80fc21f6ae684b0e4c82796f42c89aa0ed730b8a0ea66cbcc768f48"},
	{Path: "project_landing.manifest.json", Digest: "b6f06e39d9b209604af5cfd8591a2d5b15458da97cd0e044f12c2ed090f400a0"},
	{ID: "mandate", Path: "project_mandate.md", Digest: "b5e9d03e4d4e1d77ff3933abbb3664f533d20216fb947f43aaa4e69108ff7fc1"},
	{ID: "domain", Path: "domain_entities.md", Digest: "65b716f62ccb9e3bb97f4c0f6f0371c83714df77329eda0da11c4b958da184c3"},
	{ID: "constitution", Path: "project_constitution.md", Digest: "8dcecb74cc6f54e65039d3707aa125c99450a06740360060a30abd8b99e3fc36"},
	{ID: "roadmap", Path: "system_roadmap.md", Digest: "4bc43e82949f512cd7c9c4d7f8ba559864d2ba41d23eceeb33312daa3ecdf2ed"},
	{ID: "scope-policy", Path: "policies/scope_subscope_governance.md", Digest: "2ffd7fbc70b9df99f84d01300b26b918e36c0713bc3ae5757c66e7956d4d5391"},
	{ID: "genesis", Path: "todos/active/builder-genesis.md", Digest: "662bb5189567bc1be1d03a148c516bca8d6b39a4270f77543995d5cb2c523908"},
}

const approvedFingerprint = "9f634142c941e75c80dbdbe78074fdf60f13467057ec9912d3ca12394c02b606"

func fingerprint(files []approvedFile) string {
	ordered := append([]approvedFile(nil), files...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Path < ordered[j].Path })
	h := sha256.New()
	for _, file := range ordered {
		fmt.Fprintf(h, "%s\t%s\n", file.Path, file.Digest)
	}
	return hex.EncodeToString(h.Sum(nil))
}
