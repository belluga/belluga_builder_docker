package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type previewBundle struct {
	commit      string
	fingerprint string
	files       map[string][]byte
	byID        map[string]approvedFile
	localState  string
}

const maxDocumentBytes int64 = 1 << 20

type landingResponse struct {
	Commit             string          `json:"commit"`
	LandingVersion     string          `json:"landing_version"`
	AccessMode         string          `json:"access_mode"`
	RemoteVerification string          `json:"remote_verification"`
	LocalIntegrity     string          `json:"local_integrity"`
	Fingerprint        string          `json:"bundle_fingerprint"`
	Documents          []documentEntry `json:"documents"`
}

type documentEntry struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type documentResponse struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Path     string `json:"path"`
	Commit   string `json:"commit"`
	Markdown string `json:"markdown"`
}

type landingManifest struct {
	Landing struct {
		Version string `json:"document_version"`
	} `json:"landing"`
	Source struct {
		Digest string `json:"digest"`
		Inputs []struct {
			Path   string `json:"path"`
			Digest string `json:"content_digest"`
		} `json:"inputs"`
	} `json:"source_snapshot"`
	Assets struct {
		Digest string            `json:"digest"`
		Inputs []json.RawMessage `json:"inputs"`
	} `json:"asset_snapshot"`
}

func loadBundle(root string) (*previewBundle, error) {
	if err := rejectBundleRoot(root); err != nil {
		return nil, err
	}
	lockBytes, err := readBoundedFile(filepath.Join(root, "bundle.json"), 64<<10)
	if err != nil {
		return nil, err
	}
	var lock bundleLock
	if err := json.Unmarshal(lockBytes, &lock); err != nil {
		return nil, err
	}
	if lock.Commit != approvedCommit || lock.Fingerprint != approvedFingerprint || lock.Fingerprint != fingerprint(lock.Files) || !sameInventory(lock.Files) {
		return nil, errors.New("bundle identity mismatch")
	}
	if err := rejectExtraFiles(filepath.Join(root, "foundation")); err != nil {
		return nil, err
	}
	b := &previewBundle{commit: lock.Commit, fingerprint: lock.Fingerprint, files: make(map[string][]byte), byID: make(map[string]approvedFile), localState: "matching"}
	for _, file := range approvedFiles {
		path := filepath.Join(root, "foundation", filepath.FromSlash(file.Path))
		content, err := readBoundedFile(path, maxDocumentBytes)
		if err != nil {
			return nil, err
		}
		digest := sha256.Sum256(content)
		if hex.EncodeToString(digest[:]) != file.Digest {
			return nil, errors.New("bundle content differs from approved inventory")
		}
		b.files[file.Path] = append([]byte(nil), content...)
		if file.ID != "" {
			b.byID[file.ID] = file
		}
	}
	if err := b.evaluateManifest(); err != nil {
		return nil, err
	}
	return b, nil
}

func readBoundedFile(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > limit {
		return nil, errors.New("bundle file is missing, not regular, or exceeds size limit")
	}
	return os.ReadFile(path)
}

func rejectBundleRoot(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 2 || entries[0].Name() != "bundle.json" || entries[1].Name() != "foundation" || !entries[1].IsDir() {
		return errors.New("bundle root is incomplete or contains extra files")
	}
	return nil
}

func sameInventory(got []approvedFile) bool {
	if len(got) != len(approvedFiles) {
		return false
	}
	for i, want := range approvedFiles {
		if got[i] != want {
			return false
		}
	}
	return true
}

func (b *previewBundle) evaluateManifest() error {
	var manifest landingManifest
	if err := json.Unmarshal(b.files["project_landing.manifest.json"], &manifest); err != nil {
		return err
	}
	if manifest.Landing.Version == "" || len(manifest.Source.Inputs) != 6 || len(manifest.Assets.Inputs) != 0 || manifest.Assets.Digest != "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		return errors.New("manifest shape is not approved")
	}
	if !validDigest(manifest.Source.Digest) {
		return errors.New("manifest snapshot digest is malformed")
	}
	allowed := make(map[string]bool)
	for _, file := range approvedFiles {
		if file.ID != "" && file.ID != "landing" {
			allowed[file.Path] = true
		}
	}
	inputs := make([]approvedFile, 0, len(manifest.Source.Inputs))
	seen := make(map[string]bool, len(manifest.Source.Inputs))
	for _, input := range manifest.Source.Inputs {
		if !allowed[input.Path] || !validDigest(input.Digest) || seen[input.Path] {
			return errors.New("manifest input is outside approved catalog")
		}
		seen[input.Path] = true
		if _, exists := b.files[input.Path]; !exists {
			return errors.New("manifest input is missing")
		}
		inputs = append(inputs, approvedFile{Path: input.Path, Digest: strings.TrimPrefix(input.Digest, "sha256:")})
		actual := sha256.Sum256(b.files[input.Path])
		if hex.EncodeToString(actual[:]) != strings.TrimPrefix(input.Digest, "sha256:") {
			b.localState = "stale"
		}
	}
	if len(seen) != len(allowed) {
		return errors.New("manifest input catalog is incomplete")
	}
	if digestSnapshot(inputs) != manifest.Source.Digest {
		b.localState = "stale"
	}
	return nil
}

func validDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func digestSnapshot(inputs []approvedFile) string {
	ordered := append([]approvedFile(nil), inputs...)
	// The source snapshot is keyed by path and hashes the raw document bytes.
	for i := range ordered {
		ordered[i].Digest = strings.TrimPrefix(ordered[i].Digest, "sha256:")
	}
	return snapshotDigest(ordered)
}

func snapshotDigest(inputs []approvedFile) string {
	ordered := append([]approvedFile(nil), inputs...)
	// Reuse the same bytewise lexical ordering as the published manifest algorithm.
	for i := 0; i < len(ordered); i++ {
		for j := i + 1; j < len(ordered); j++ {
			if ordered[j].Path < ordered[i].Path {
				ordered[i], ordered[j] = ordered[j], ordered[i]
			}
		}
	}
	var builder strings.Builder
	for _, input := range ordered {
		builder.WriteString(input.Path)
		builder.WriteByte('\t')
		builder.WriteString(input.Digest)
		builder.WriteByte('\n')
	}
	digest := sha256.Sum256([]byte(builder.String()))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func (b *previewBundle) apiHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/preview" {
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
		var manifest landingManifest
		if err := json.Unmarshal(b.files["project_landing.manifest.json"], &manifest); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid_manifest")
			return
		}
		response := landingResponse{Commit: b.commit, LandingVersion: manifest.Landing.Version, AccessMode: "public_preview", RemoteVerification: "unverifiable", LocalIntegrity: b.localState, Fingerprint: b.fingerprint}
		for _, id := range []string{"landing", "mandate", "domain", "constitution", "roadmap", "scope-policy", "genesis"} {
			response.Documents = append(response.Documents, documentEntry{ID: id, Title: titleFor(id)})
		}
		writeJSON(w, http.StatusOK, response)
	})
}

func (b *previewBundle) documentHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/api/preview/documents/")
		file, exists := b.byID[id]
		if !exists || strings.Contains(id, "/") || id == "" {
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
		writeJSON(w, http.StatusOK, documentResponse{ID: id, Title: titleFor(id), Path: file.Path, Commit: b.commit, Markdown: string(b.files[file.Path])})
	})
}

func titleFor(id string) string {
	titles := map[string]string{"landing": "Project Landing", "mandate": "Project Mandate", "domain": "Domain Entities", "constitution": "Project Constitution", "roadmap": "System Roadmap", "scope-policy": "Scope Governance", "genesis": "Builder Genesis TODO"}
	return titles[id]
}

func staticHandler(root string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
		name := strings.TrimPrefix(filepath.Clean("/"+r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		} else if strings.HasPrefix(name, "documents/") {
			id := strings.TrimPrefix(name, "documents/")
			if id == "" || len(id) > 80 || strings.Contains(id, "/") || strings.Contains(id, "..") {
				writeError(w, http.StatusNotFound, "not_found")
				return
			}
			name = "index.html"
		} else if strings.HasPrefix(name, "assets/") {
			if strings.Contains(name, "..") {
				writeError(w, http.StatusNotFound, "not_found")
				return
			}
		} else {
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
		path := filepath.Join(root, name)
		if info, err := os.Stat(path); err != nil || info.IsDir() {
			if strings.Contains(name, ".") {
				writeError(w, http.StatusNotFound, "not_found")
				return
			}
			path = filepath.Join(root, "index.html")
		}
		http.ServeFile(w, r, path)
	})
}

func rejectExtraFiles(root string) error {
	wanted := make(map[string]bool, len(approvedFiles))
	for _, file := range approvedFiles {
		wanted[filepath.ToSlash(file.Path)] = true
	}
	seen := make(map[string]bool, len(wanted))
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return fs.ErrInvalid
		}
		relative = filepath.ToSlash(relative)
		if entry.IsDir() {
			if relative == "." {
				return nil
			}
			for wantedPath := range wanted {
				if strings.HasPrefix(wantedPath, relative+"/") {
					return nil
				}
			}
			return fs.ErrInvalid
		}
		if !entry.Type().IsRegular() {
			return fs.ErrInvalid
		}
		if !wanted[relative] {
			return fs.ErrInvalid
		}
		seen[relative] = true
		return nil
	})
	if err != nil || len(seen) != len(wanted) {
		return errors.New("bundle is incomplete or contains extra files")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}
