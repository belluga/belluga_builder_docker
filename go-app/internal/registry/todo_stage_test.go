package registry

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMongoBornAdjacentStartRequiresCurrentApproval(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	ref := "builder/todo-a/content/1"
	approval := TODOProofReference{Kind: "owner-approval", Source: "mongo-content-revision", Reference: ref, SourceDigest: digest, VerificationState: "verified"}
	record := TODORecord{ID: "todo-a", Stage: "Implementation-Approval-Pending", StageVerification: "verified", VerifiedTransitionRef: &approval, Revision: 2}
	request := TODOActionRequest{RequestedStage: "Ready-for-Implementation", ProofReferences: []TODOProofReference{}, WaiverReferences: []TODOProofReference{}}
	proof, err := mongoBornStageProof(record, request, digest, ref)
	if err != nil || proof != approval {
		t.Fatalf("approved Ready edge = %+v, %v", proof, err)
	}
	record.Stage = "Ready-for-Implementation"
	record.Revision++
	request.RequestedStage = "In-Progress"
	proof, err = mongoBornStageProof(record, request, digest, ref)
	if err != nil || proof.Kind != "work-started" || proof.Source != "mongo-todo-api" || proof.SourceDigest != digest {
		t.Fatalf("governed start edge = %+v, %v", proof, err)
	}
	record.Stage = "Implementation-Approval-Pending"
	record.VerifiedTransitionRef = nil
	if _, err := mongoBornStageProof(record, TODOActionRequest{RequestedStage: "Ready-for-Implementation"}, digest, ref); err == nil {
		t.Fatal("Ready accepted without owner approval")
	}
	record.VerifiedTransitionRef = &approval
	if _, err := mongoBornStageProof(record, TODOActionRequest{RequestedStage: "Ready-for-Implementation"}, digest+"changed", ref); err == nil {
		t.Fatal("Ready accepted stale content approval")
	}
	if _, err := mongoBornStageProof(record, TODOActionRequest{RequestedStage: "Completed"}, digest, ref); err == nil {
		t.Fatal("Mongo-born TODO skipped adjacent lifecycle stages")
	}
}

func TestSelectedLocalProofRequiresExactMatrixRunAndOwnerAcceptance(t *testing.T) {
	store := &MongoStore{}
	if err := store.ConfigureLocalMatrix(filepath.Join("..", "..", "config", "selected_todo_local_checks.json")); err != nil {
		t.Fatal(err)
	}
	id := "builder-company-tenant-mongo-registry"
	digest := "sha256:" + strings.Repeat("a", 64)
	now := time.Now().UTC()
	result := &LocalProofResult{
		SchemaVersion: "builder-local-proof-result-v1", TODOID: id,
		RunID: "11111111-2222-4333-8444-555555555555", MatrixDigest: store.localMatrix.digest,
		StartedAt: now.Add(-time.Minute).Format(time.RFC3339Nano), FinishedAt: now.Format(time.RFC3339Nano),
		Before: localProofSnapshot{BuilderHead: strings.Repeat("a", 40), PACEDHead: strings.Repeat("b", 40), BuilderInputsDigest: digest, PACEDInputsDigest: digest},
	}
	result.After = result.Before
	result.OutsideUntrackedBefore = map[string]map[string]string{"builder": {}, "paced": {}}
	result.OutsideUntrackedAfter = map[string]map[string]string{"builder": {}, "paced": {}}
	for _, command := range store.localMatrix.RequiredBy[id] {
		result.Commands = append(result.Commands, localProofCommand{ID: command, ExitCode: 0})
	}
	seal := func() {
		data, _ := json.Marshal(result)
		var value map[string]any
		_ = json.Unmarshal(data, &value)
		delete(value, "result_digest")
		canonical, _ := json.Marshal(value)
		result.ResultDigest = sourceHash(canonical)
	}
	seal()
	acceptance := &OwnerAcceptance{RunID: result.RunID, ResultDigest: result.ResultDigest, AcceptedBy: "owner"}
	if proof, err := store.validateLocalProof(id, result, acceptance); err != nil || proof.Kind != "local-run-accepted" {
		t.Fatalf("valid proof = %+v, %v", proof, err)
	}
	result.TODOID = "builder-mongo-todo-history-storage-implementation"
	result.Commands = result.Commands[:len(store.localMatrix.DefaultRequired)]
	seal()
	acceptance.ResultDigest = result.ResultDigest
	if proof, err := store.validateLocalProof(result.TODOID, result, acceptance); err != nil || proof.Kind != "local-run-accepted" {
		t.Fatalf("individually admitted TODO proof = %+v, %v", proof, err)
	}
	result.TODOID = id
	result.Commands = append(result.Commands, localProofCommand{ID: "P-TEST", ExitCode: 0})
	seal()
	acceptance.ResultDigest = result.ResultDigest
	if _, err := store.validateLocalProof(id, result, nil); err == nil {
		t.Fatal("missing separate owner acceptance was accepted")
	}
	wrongAcceptance := *acceptance
	wrongAcceptance.ResultDigest = digest
	if _, err := store.validateLocalProof(id, result, &wrongAcceptance); err == nil {
		t.Fatal("wrong owner result digest was accepted")
	}
	result.Commands[0].ExitCode = 1
	seal()
	acceptance.ResultDigest = result.ResultDigest
	if _, err := store.validateLocalProof(id, result, acceptance); err == nil {
		t.Fatal("failed fixed command was accepted")
	}
	result.Commands[0].ExitCode = 0
	seal()
	acceptance.ResultDigest = result.ResultDigest
	result.After.BuilderInputsDigest = "sha256:" + strings.Repeat("b", 64)
	seal()
	acceptance.ResultDigest = result.ResultDigest
	if _, err := store.validateLocalProof(id, result, acceptance); err == nil {
		t.Fatal("changed test input was accepted")
	}
}

func TestRetainedTODOOperationRetryRejectsChangedInputAndOldRevision(t *testing.T) {
	record := TODORecord{ID: "t-cf5c0ffd07c4f50d", ProjectID: "boora", Revision: 4, Stage: "Completed", StageVerification: "verified", LastOperation: &TODOOperationState{ID: "op-1", Kind: "transition", RequestDigest: "sha256:" + strings.Repeat("a", 64), ResultRevision: 4}}
	if response, ok, err := exactTODOOperationRetry("belluga-solutions", record, "op-1", "transition", record.LastOperation.RequestDigest, 3); !ok || err != nil || !response.NoOp || response.CompanyID != "belluga-solutions" {
		t.Fatalf("retry = %+v %v %v", response, ok, err)
	}
	if _, ok, err := exactTODOOperationRetry("belluga-solutions", record, "op-1", "transition", "sha256:"+strings.Repeat("b", 64), 3); !ok || err == nil {
		t.Fatal("same ID with changed input was accepted")
	}
	if _, ok, err := exactTODOOperationRetry("belluga-solutions", record, "op-1", "transition", record.LastOperation.RequestDigest, 2); !ok || err == nil {
		t.Fatal("old revision was accepted")
	}
}

func TestCoordinatorCountsAsExecutableAndOnlyVerifiedStageIsEffective(t *testing.T) {
	record := TODORecord{ID: "t-9936500c9582e07e", Title: "Release coordination", Objective: "Coordinate children", NextExactStep: "Confirm publication", Stage: "Planning", StageVerification: "unverified", Qualifiers: []string{}}
	view := roadmapTODOViewFromRecord(record)
	if view.Kind != "executable" || view.DeliveryStageState != "unverified" || view.DeliveryStage != "" {
		t.Fatalf("unverified coordinator view = %+v", view)
	}
	record.Stage = "Completed"
	record.StageVerification = "verified"
	record.VerifiedTransitionRef = &TODOProofReference{Kind: "production-deployment", Source: "private-reader", Reference: "17@root", SourceDigest: "sha256:" + strings.Repeat("a", 64), VerificationState: "verified"}
	view = roadmapTODOViewFromRecord(record)
	if view.Kind != "executable" || view.DeliveryStage != "Completed" || view.DeliveryStageState != "verified" {
		t.Fatalf("verified coordinator view = %+v", view)
	}
}
