package registry

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"regexp"
	"time"
)

var localRunIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type localProofMatrix struct {
	SchemaVersion   string   `json:"schema_version"`
	DefaultRequired []string `json:"default_required"`
	Commands        []struct {
		ID   string   `json:"id"`
		Argv []string `json:"argv"`
	} `json:"commands"`
	RequiredBy map[string][]string `json:"required_by"`
	digest     string
}

type localProofSnapshot struct {
	BuilderHead         string `json:"builder_head"`
	PACEDHead           string `json:"paced_head"`
	BuilderInputsDigest string `json:"builder_inputs_digest"`
	PACEDInputsDigest   string `json:"paced_inputs_digest"`
}

type localProofCommand struct {
	ID       string `json:"id"`
	ExitCode int    `json:"exit_code"`
}

type LocalProofResult struct {
	SchemaVersion          string                       `json:"schema_version"`
	TODOID                 string                       `json:"todo_id"`
	RunID                  string                       `json:"run_id"`
	MatrixDigest           string                       `json:"matrix_digest"`
	StartedAt              string                       `json:"started_at"`
	FinishedAt             string                       `json:"finished_at"`
	Before                 localProofSnapshot           `json:"before"`
	After                  localProofSnapshot           `json:"after"`
	OutsideUntrackedBefore map[string]map[string]string `json:"outside_untracked_before"`
	OutsideUntrackedAfter  map[string]map[string]string `json:"outside_untracked_after"`
	Commands               []localProofCommand          `json:"commands"`
	ResultDigest           string                       `json:"result_digest"`
}

type OwnerAcceptance struct {
	RunID        string `json:"run_id"`
	ResultDigest string `json:"result_digest"`
	AcceptedBy   string `json:"accepted_by"`
}

func sourceHash(data []byte) string {
	hash := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(hash[:])
}

func (m *MongoStore) ConfigureLocalMatrix(path string) error {
	if path == "" {
		return errors.New("missing matrix path")
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 || len(data) > 32<<10 {
		return errors.New("local proof matrix unavailable")
	}
	var matrix localProofMatrix
	if json.Unmarshal(data, &matrix) != nil || matrix.SchemaVersion != "builder-local-proof-v1" || len(matrix.Commands) == 0 || len(matrix.DefaultRequired) == 0 || len(matrix.RequiredBy) != 3 {
		return errors.New("local proof matrix invalid")
	}
	commandsByID := make(map[string]bool, len(matrix.Commands))
	for _, command := range matrix.Commands {
		if command.ID == "" || len(command.Argv) == 0 || commandsByID[command.ID] {
			return errors.New("local proof matrix has invalid command")
		}
		commandsByID[command.ID] = true
	}
	for _, id := range matrix.DefaultRequired {
		if !commandsByID[id] {
			return errors.New("local proof matrix has unknown default command")
		}
	}
	for id, commands := range matrix.RequiredBy {
		if id != "builder-company-tenant-mongo-registry" && id != "builder-mongo-v0-7-1-todo-migration" && id != "builder-mongo-selected-todo-stage-verification" {
			return errors.New("local proof matrix has unapproved TODO")
		}
		if len(commands) == 0 {
			return errors.New("local proof matrix has empty requirement")
		}
		for _, command := range commands {
			if !commandsByID[command] {
				return errors.New("local proof matrix has unknown required command")
			}
		}
	}
	matrix.digest = sourceHash(data)
	m.localMatrix = &matrix
	return nil
}

func (m *MongoStore) validateLocalProof(todoID string, result *LocalProofResult, acceptance *OwnerAcceptance) (TODOProofReference, error) {
	if m.localMatrix == nil || result == nil || acceptance == nil {
		return TODOProofReference{}, todoTeach("local_proof/owner_acceptance", "provide the fixed PACED local result and separate owner acceptance for this TODO", ErrConflict)
	}
	required := m.localMatrix.RequiredBy[todoID]
	if len(required) == 0 {
		required = m.localMatrix.DefaultRequired
	}
	if result.SchemaVersion != "builder-local-proof-result-v1" || result.TODOID != todoID || !localRunIDPattern.MatchString(result.RunID) || result.MatrixDigest != m.localMatrix.digest || len(result.Commands) != len(required) {
		return TODOProofReference{}, todoTeach("local_proof", "match this TODO and the current fixed command matrix", ErrConflict)
	}
	for i, id := range required {
		if result.Commands[i].ID != id || result.Commands[i].ExitCode != 0 {
			return TODOProofReference{}, todoTeach("local_proof.commands", "pass every fixed command for this TODO in matrix order", ErrConflict)
		}
	}
	if result.Before != result.After || !validSourceDigest(result.Before.BuilderInputsDigest) || !validSourceDigest(result.Before.PACEDInputsDigest) || len(result.Before.BuilderHead) != 40 || len(result.Before.PACEDHead) != 40 {
		return TODOProofReference{}, todoTeach("local_proof.inputs", "show unchanged Builder/PACED heads and approved-path content during the run", ErrConflict)
	}
	if len(result.OutsideUntrackedBefore) != 2 || result.OutsideUntrackedBefore["builder"] == nil || result.OutsideUntrackedBefore["paced"] == nil || !reflect.DeepEqual(result.OutsideUntrackedBefore, result.OutsideUntrackedAfter) {
		return TODOProofReference{}, todoTeach("local_proof.outside_untracked", "leave outside-scope untracked files unchanged during the run", ErrConflict)
	}
	started, startErr := time.Parse(time.RFC3339Nano, result.StartedAt)
	finished, finishErr := time.Parse(time.RFC3339Nano, result.FinishedAt)
	if startErr != nil || finishErr != nil || finished.Before(started) || finished.Sub(started) > 2*time.Hour || time.Since(finished) > 24*time.Hour || finished.After(time.Now().Add(time.Minute)) {
		return TODOProofReference{}, todoTeach("local_proof.time", "provide a recent bounded real run time", ErrConflict)
	}
	data, err := json.Marshal(result)
	if err != nil {
		return TODOProofReference{}, ErrInvalid
	}
	var values map[string]any
	if json.Unmarshal(data, &values) != nil {
		return TODOProofReference{}, ErrInvalid
	}
	delete(values, "result_digest")
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if encoder.Encode(values) != nil {
		return TODOProofReference{}, ErrInvalid
	}
	canonical := bytes.TrimSpace(encoded.Bytes())
	if result.ResultDigest != sourceHash(canonical) || acceptance.RunID != result.RunID || acceptance.ResultDigest != result.ResultDigest || acceptance.AcceptedBy == "" {
		return TODOProofReference{}, todoTeach("owner_acceptance", "bind explicit owner acceptance to this exact run ID and result digest", ErrConflict)
	}
	return TODOProofReference{Kind: "local-run-accepted", Source: "paced-local-runner", Reference: result.RunID, SourceDigest: result.ResultDigest, VerificationState: "verified"}, nil
}
