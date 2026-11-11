package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestKnowledgeOnlyContractIsExplicitAndBoundedByExistingTransitionSize(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	valid := &TODODeliveryContract{Kind: "knowledge-only", Targets: []TODOKnowledgeTarget{{Family: FamilyModule, RecordID: "accounts", GenerationID: "g1", BaselineRevision: 1, BaselineDigest: digest, ChangeKind: "edit"}}}
	if err := validateTODODeliveryContract(valid); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*TODODeliveryContract){
		"implicit mode":                   func(v *TODODeliveryContract) { v.Kind = "" },
		"duplicate target":                func(v *TODODeliveryContract) { v.Targets = append(v.Targets, v.Targets[0]) },
		"missing baseline digest":         func(v *TODODeliveryContract) { v.Targets[0].BaselineDigest = "" },
		"creation with existing baseline": func(v *TODODeliveryContract) { v.Targets[0].ChangeKind = "create" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := *valid
			candidate.Targets = append([]TODOKnowledgeTarget{}, valid.Targets...)
			mutate(&candidate)
			if err := validateTODODeliveryContract(&candidate); err == nil {
				t.Fatal("invalid Knowledge-only contract accepted")
			}
		})
	}
	large := &TODODeliveryContract{Kind: "knowledge-only", Targets: make([]TODOKnowledgeTarget, 1000)}
	for i := range large.Targets {
		large.Targets[i] = TODOKnowledgeTarget{Family: FamilyModule, RecordID: fmt.Sprintf("module-%04d", i), GenerationID: "g1", BaselineRevision: 1, BaselineDigest: digest, ChangeKind: "edit"}
	}
	if err := validateTODODeliveryContract(large); err == nil || !strings.Contains(err.Error(), "128 KiB") {
		t.Fatalf("oversized proof not rejected before approval: %v", err)
	}
}

func TestKnowledgeClaimCapacityAcceptsFeasibleDistributedCoverage(t *testing.T) {
	content := TODOContent{Delivery: &TODODeliveryContract{Kind: "knowledge-only"}}
	for i := 0; i < 200; i++ {
		content.Delivery.Targets = append(content.Delivery.Targets, TODOKnowledgeTarget{
			Family: FamilyModule, RecordID: fmt.Sprintf("module-%03d", i), GenerationID: "g1", ChangeKind: "create",
		})
	}
	for i := 0; i < 40; i++ {
		content.Criteria = append(content.Criteria, TODOContentItem{ID: fmt.Sprintf("criterion-%02d-%s", i, strings.Repeat("c", 40)), Text: "Covered by a delivered revision."})
		content.ValidationScenarios = append(content.ValidationScenarios, TODOValidationScenario{ID: fmt.Sprintf("scenario-%02d-%s", i, strings.Repeat("s", 40)), Method: "Verify the exact revision.", Expected: "The revision is verified."})
	}
	if err := validateKnowledgeClaimCapacity(content); err != nil {
		t.Fatalf("a feasible distributed claim was rejected by preflight: %v", err)
	}
	large := TODOContent{Delivery: &TODODeliveryContract{Kind: "knowledge-only"}}
	for i := 0; i < 64; i++ {
		criterionID := fmt.Sprintf("criterion-%02d-%s", i, strings.Repeat("c", 112))
		scenarioID := fmt.Sprintf("scenario-%02d-%s", i, strings.Repeat("s", 113))
		large.Criteria = append(large.Criteria, TODOContentItem{ID: criterionID, Text: "Covered by delivery."})
		large.ValidationScenarios = append(large.ValidationScenarios, TODOValidationScenario{ID: scenarioID, CriterionIDs: []string{criterionID}, Method: "Check revision.", Expected: "Verified."})
	}
	found := false
	for i := 0; i < 500; i++ {
		large.Delivery.Targets = append(large.Delivery.Targets, TODOKnowledgeTarget{Family: FamilyModule, RecordID: fmt.Sprintf("module-%03d", i), GenerationID: "g1", ChangeKind: "create"})
		if err := validateTODODeliveryContract(large.Delivery); err != nil {
			break
		}
		if err := validateKnowledgeClaimCapacity(large); err != nil {
			if !strings.Contains(err.Error(), "128 KiB") {
				t.Fatalf("wrong capacity rejection: %v", err)
			}
			found = true
			break
		}
	}
	if !found {
		t.Fatal("no valid target set exposed a required-coverage overflow beyond the target-only bound")
	}
}

func TestKnowledgeSetDigestUsesOrderedExactIdentities(t *testing.T) {
	base := TODOKnowledgeVerifiedItem{TODOKnowledgeTarget: TODOKnowledgeTarget{Family: FamilyModule, RecordID: "accounts", GenerationID: "g1", BaselineRevision: 1, BaselineDigest: "sha256:" + strings.Repeat("a", 64), ChangeKind: "edit"}, ToRevision: 2, ToDigest: "sha256:" + strings.Repeat("b", 64), EventAt: time.Unix(123, 0).UTC(), CriterionIDs: []string{"c-1"}, ScenarioIDs: []string{"v-1"}}
	first := knowledgeClaimDigest([]TODOKnowledgeVerifiedItem{base})
	copy := base
	copy.ToRevision++
	if first == knowledgeClaimDigest([]TODOKnowledgeVerifiedItem{copy}) {
		t.Fatal("delivered revision did not affect set digest")
	}
	copy = base
	copy.CriterionIDs = []string{"c-2"}
	if first == knowledgeClaimDigest([]TODOKnowledgeVerifiedItem{copy}) {
		t.Fatal("criterion mapping did not affect set digest")
	}
	left, right := base, base
	left.CriterionIDs, left.ScenarioIDs = []string{"a"}, []string{"b", "c"}
	right.CriterionIDs, right.ScenarioIDs = []string{"a", "b"}, []string{"c"}
	if knowledgeClaimDigest([]TODOKnowledgeVerifiedItem{left}) == knowledgeClaimDigest([]TODOKnowledgeVerifiedItem{right}) {
		t.Fatal("criterion/scenario boundary collision in acceptance digest")
	}
}

func TestRoadmapKnowledgeCompletionIsDistinctFromCodeDelivery(t *testing.T) {
	proof := &TODOKnowledgeDeliveryProof{SetDigest: "sha256:" + strings.Repeat("a", 64)}
	ref := &TODOProofReference{Kind: "knowledge-final-acceptance"}
	base := TODORecord{ID: "documentary-todo", ProjectID: "builder", Stage: "Completed", StageVerification: "verified", Content: &TODOContent{Delivery: &TODODeliveryContract{Kind: "knowledge-only"}}, KnowledgeDeliveryProof: proof, VerifiedTransitionRef: ref}
	view := roadmapTODOViewFromRecord(base)
	if view.DeliveryKind != "knowledge-only" || !view.DocumentaryCompletion || view.DeliveryStage != "Completed" {
		t.Fatalf("verified documentary completion missing: %+v", view)
	}
	base.StageVerification = ""
	if roadmapTODOViewFromRecord(base).DocumentaryCompletion {
		t.Fatal("unverified documentary completion displayed")
	}
	base.StageVerification = "verified"
	base.Content.Delivery.Kind = "code"
	view = roadmapTODOViewFromRecord(base)
	if view.DeliveryKind != "" || view.DocumentaryCompletion {
		t.Fatalf("code-backed TODO displayed as documentary completion: %+v", view)
	}
}

func TestMongoKnowledgeDeliveryVerifiesExactImmutableMutation(t *testing.T) {
	uri := os.Getenv("REGISTRY_TEST_MONGODB_URI")
	if uri == "" {
		if os.Getenv("REGISTRY_REQUIRE_MONGO_INTEGRATION") == "1" {
			t.Fatal("required Mongo replica-set fixture is unavailable")
		}
		t.Skip("isolated MongoDB replica-set fixture not configured")
	}
	ctx := context.Background()
	stamp := fmt.Sprintf("knowledge_delivery_%d", time.Now().UnixNano())
	store, err := OpenMongo(ctx, uri, stamp)
	if err != nil {
		t.Fatal(err)
	}
	store.tenantPrefix = stamp
	t.Cleanup(func() {
		_ = store.client.Database(store.tenantDatabase("company-knowledge")).Drop(context.Background())
		_ = store.control.Drop(context.Background())
		_ = store.Close(context.Background())
	})
	project, err := registerTestProjectWithBinding(ctx, store, Registration{CompanyID: "company-knowledge", CompanyName: "Knowledge", ProjectID: "builder", ProjectName: "Builder", Binding: validTestBinding()})
	if err != nil {
		t.Fatal(err)
	}
	input := CanonicalImport{ProjectID: "builder", GenerationID: "g1", SourceRevision: strings.Repeat("a", 40), OperationID: "import-knowledge", Actor: "owner", Records: []CanonicalImportRecord{
		canonicalTestRecord(FamilyMandate, "builder", `{"purpose":"Build","scope":["web"]}`),
		canonicalTestRecord(FamilyDomain, "builder", `{"entities":[{"id":"Project","name":"Project","description":""}],"relations":[{"id":"OWNS","from":"Project","to":"Project","kind":"owns"}],"distinctions":[]}`),
		canonicalTestRecord(FamilyConstitution, "builder", `{"promotion_mode":"mvp","capability_namespaces":["go"],"rules":[{"id":"tenant-access","status":"current","statement":"Scope reads"}]}`),
		canonicalTestRecord(FamilyRoadmap, "builder", `{"stages":[{"id":"launch","name":"Launch","status":"planned"}]}`),
		canonicalTestRecord(FamilyModule, "projects", `{"purpose":"Project work","decisions":[{"id":"DEC-ART-01","status":"current","statement":"Module owns routes"}],"schemas":[{"id":"project","name":"projects","kind":"collection","fields":[{"name":"name","type":"string"}]}]}`),
		canonicalTestRecord(FamilyPolicy, "access-policy", `{"rules":[{"id":"retention","status":"current","statement":"Keep audit records"}],"ownership":[{"owner_kind":"module","owner_id":"projects","decision_id":"DEC-ART-01"}]}`),
	}}
	if _, err := store.ImportCanonicalDraft(ctx, "company-knowledge", input); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ActivateCanonical(ctx, "company-knowledge", "builder", "g1", "activate-knowledge", "owner"); err != nil {
		t.Fatal(err)
	}
	project, err = store.Project(ctx, "company-knowledge", "builder")
	if err != nil {
		t.Fatal(err)
	}
	// A tenant created before this TODO may lack the proof index. The first
	// documentary transition must add it before issuing a hinted query.
	indexDB := store.client.Database(store.tenantDatabase("company-knowledge"))
	if err := indexDB.Collection(canonicalEvents).Indexes().DropOne(ctx, "canonical_events_record_revision"); err != nil {
		t.Fatal(err)
	}
	if err := store.ensureKnowledgeProofIndex(ctx, store.tenantDatabase("company-knowledge")); err != nil {
		t.Fatalf("existing Company proof index migration: %v", err)
	}
	// A long-lived record must not make a narrow proof scan its entire event
	// timeline just because no temporal offender exists.
	perfDB := store.client.Database(store.tenantDatabase("company-knowledge"))
	perfEvents := make([]any, 500)
	oldTime, newTime := time.Now().Add(-time.Hour).UTC(), time.Now().UTC()
	for revision := 1; revision <= len(perfEvents); revision++ {
		at := oldTime
		if revision == len(perfEvents) {
			at = newTime
		}
		perfEvents[revision-1] = bson.D{{Key: "project_id", Value: "builder"}, {Key: "generation_id", Value: "g1"}, {Key: "family", Value: FamilyModule}, {Key: "record_id", Value: "perf-history"}, {Key: "revision", Value: int64(revision)}, {Key: "occurred_at", Value: at}, {Key: "kind", Value: "content_changed"}, {Key: "operation_id", Value: fmt.Sprintf("perf-%d", revision)}}
	}
	if _, err := perfDB.Collection(canonicalEvents).InsertMany(ctx, perfEvents); err != nil {
		t.Fatalf("seed long event history: %v", err)
	}
	for _, tc := range []struct {
		name, comparison string
		from, to         int64
		boundary         time.Time
	}{
		{"prospective", "$lte", 499, 500, oldTime},
		{"retrospective", "$gte", 1, 2, newTime},
	} {
		t.Run("bounded-temporal-index-"+tc.name, func(t *testing.T) {
			filter := bson.D{{Key: "project_id", Value: "builder"}, {Key: "generation_id", Value: "g1"}, {Key: "$or", Value: bson.A{bson.D{{Key: "family", Value: FamilyModule}, {Key: "record_id", Value: "perf-history"}, {Key: "revision", Value: bson.D{{Key: "$gt", Value: tc.from}, {Key: "$lte", Value: tc.to}}}, {Key: "occurred_at", Value: bson.D{{Key: tc.comparison, Value: tc.boundary}}}}}}}
			command := bson.D{{Key: "explain", Value: bson.D{{Key: "find", Value: canonicalEvents}, {Key: "filter", Value: filter}, {Key: "hint", Value: "canonical_events_record_revision"}, {Key: "limit", Value: 1}}}, {Key: "verbosity", Value: "executionStats"}}
			var explained bson.Raw
			if err := perfDB.RunCommand(ctx, command).Decode(&explained); err != nil {
				t.Fatalf("explain temporal proof: %v", err)
			}
			keys := explained.Lookup("executionStats").Document().Lookup("totalKeysExamined").Int32()
			if keys > 5 {
				t.Fatalf("temporal proof examined more than the revision interval: %d", keys)
			}
		})
	}
	base, err := store.ReadCanonical(ctx, "company-knowledge", "builder", FamilyModule, "projects")
	if err != nil {
		t.Fatal(err)
	}
	target := TODOKnowledgeTarget{Family: FamilyModule, RecordID: "projects", GenerationID: "g1", BaselineRevision: base.Revision, BaselineDigest: base.ContentDigest, ChangeKind: "edit"}
	if err := store.validateKnowledgeApprovalTargets(ctx, "company-knowledge", project, &TODODeliveryContract{Kind: "knowledge-only", Targets: []TODOKnowledgeTarget{target}}); err != nil {
		t.Fatalf("valid approval baseline rejected: %v", err)
	}
	foreign := target
	foreign.GenerationID = "other-generation"
	if err := store.validateKnowledgeApprovalTargets(ctx, "company-knowledge", project, &TODODeliveryContract{Kind: "knowledge-only", Targets: []TODOKnowledgeTarget{foreign}}); err == nil {
		t.Fatal("foreign Knowledge generation accepted at approval")
	}
	wrongBaseline := target
	wrongBaseline.BaselineDigest = "sha256:" + strings.Repeat("f", 64)
	if err := store.validateKnowledgeApprovalTargets(ctx, "company-knowledge", project, &TODODeliveryContract{Kind: "knowledge-only", Targets: []TODOKnowledgeTarget{wrongBaseline}}); err == nil {
		t.Fatal("wrong baseline digest accepted at approval")
	}
	approvalAt, workStartedAt := base.UpdatedAt, base.UpdatedAt
	time.Sleep(2 * time.Millisecond)
	changed := base
	changed.Payload = json.RawMessage(`{"purpose":"Project work with verified docs","decisions":[{"id":"DEC-ART-01","status":"current","statement":"Module owns routes"}],"schemas":[{"id":"project","name":"projects","kind":"collection","fields":[{"name":"name","type":"string"}]}]}`)
	delivered, err := store.EditCanonical(ctx, "company-knowledge", CanonicalMutation{ProjectID: "builder", Family: FamilyModule, Record: changed, ExpectedRevision: base.Revision, OperationID: "edit-knowledge", Actor: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	contentDigest := "sha256:" + strings.Repeat("c", 64)
	record := TODORecord{ID: "knowledge-todo", ProjectID: "builder", Content: &TODOContent{
		Delivery:            &TODODeliveryContract{Kind: "knowledge-only", Targets: []TODOKnowledgeTarget{target}},
		Criteria:            []TODOContentItem{{ID: "c-1"}},
		ValidationScenarios: []TODOValidationScenario{{ID: "s-1", CriterionIDs: []string{"c-1"}}},
	}}
	claim := &TODOKnowledgeDeliveryClaim{ContentDigest: contentDigest, OwnerAttributionReference: "owner accepted exact Knowledge edit", Items: []TODOKnowledgeDeliveryItem{{Family: FamilyModule, RecordID: "projects", GenerationID: "g1", ToRevision: delivered.Revision, ToDigest: delivered.ContentDigest, CriterionIDs: []string{"c-1"}, ScenarioIDs: []string{"s-1"}}}}
	proof, err := store.verifyKnowledgeDelivery(ctx, "company-knowledge", project, record, contentDigest, claim, false, approvalAt, workStartedAt)
	if err != nil || proof == nil || len(proof.Items) != 1 || proof.Items[0].ToRevision != delivered.Revision {
		t.Fatalf("exact Knowledge proof = %+v, %v", proof, err)
	}
	for name, mutate := range map[string]func(*TODORecord, *TODOKnowledgeDeliveryClaim){
		"wrong mutation kind": func(r *TODORecord, _ *TODOKnowledgeDeliveryClaim) {
			r.Content.Delivery.Targets[0].ChangeKind = "delete"
		},
		"missing criterion coverage": func(_ *TODORecord, c *TODOKnowledgeDeliveryClaim) { c.Items[0].CriterionIDs = nil },
		"wrong claim generation":     func(_ *TODORecord, c *TODOKnowledgeDeliveryClaim) { c.Items[0].GenerationID = "other-generation" },
		"wrong claim record":         func(_ *TODORecord, c *TODOKnowledgeDeliveryClaim) { c.Items[0].RecordID = "other-record" },
	} {
		t.Run(name, func(t *testing.T) {
			candidateRecord := record
			candidateContent := *record.Content
			candidateDelivery := *record.Content.Delivery
			candidateDelivery.Targets = append([]TODOKnowledgeTarget{}, record.Content.Delivery.Targets...)
			candidateContent.Delivery = &candidateDelivery
			candidateRecord.Content = &candidateContent
			candidateClaim := *claim
			candidateClaim.Items = append([]TODOKnowledgeDeliveryItem{}, claim.Items...)
			mutate(&candidateRecord, &candidateClaim)
			if _, err := store.verifyKnowledgeDelivery(ctx, "company-knowledge", project, candidateRecord, contentDigest, &candidateClaim, false, approvalAt, workStartedAt); err == nil {
				t.Fatal("invalid exact Knowledge proof accepted")
			}
		})
	}
	claim.Retrospective = true
	if _, err := store.verifyKnowledgeDelivery(ctx, "company-knowledge", project, record, contentDigest, claim, true, approvalAt, time.Time{}); err == nil {
		t.Fatal("post-approval mutation accepted as retrospective work")
	}
	if _, err := store.verifyKnowledgeDelivery(ctx, "company-knowledge", project, record, contentDigest, claim, true, time.Now().Add(time.Second), time.Time{}); err != nil {
		t.Fatalf("pre-approval historical mutation rejected: %v", err)
	}
	claim.Retrospective = false
	claim.Items[0].ToDigest = base.ContentDigest
	if _, err := store.verifyKnowledgeDelivery(ctx, "company-knowledge", project, record, contentDigest, claim, false, approvalAt, workStartedAt); err == nil {
		t.Fatal("wrong delivered digest accepted")
	}

	// Traverse the real TODO API and persist its aggregate, immutable content,
	// transition events and owner acceptance. The Knowledge edit above predates
	// approval, so this must use the explicit retrospective edge.
	db := store.client.Database(store.tenantDatabase("company-knowledge"))
	if err := store.ProvisionTODOCollection(ctx, "company-knowledge"); err != nil {
		t.Fatal(err)
	}
	if err := store.ProvisionTODOHistory(ctx, "company-knowledge"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Collection(epicsCollection).InsertOne(ctx, roadmapEpic{StorageID: bson.NewObjectID(), ProjectID: "builder", ID: "e-knowledge", Name: "Knowledge", Position: 0}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Collection(releasesCollection).InsertOne(ctx, roadmapRelease{StorageID: bson.NewObjectID(), ProjectID: "builder", ID: "r-knowledge", EpicID: "e-knowledge", Name: "Documentation", Position: 0}); err != nil {
		t.Fatal(err)
	}
	todo := mongoBornTODOFixture()
	todo.ID, todo.ReleaseID, todo.Position = "knowledge-todo", "r-knowledge", intPointer(0)
	content := validTODOContent()
	content.Delivery = &TODODeliveryContract{Kind: "knowledge-only", Targets: []TODOKnowledgeTarget{target}}
	content.Planning = &TODOPlanning{Complexity: "small", AuditTriggers: testAuditTriggers(), Assumptions: []TODOPlanningAssumption{}, Reviews: []TODOPlanningReview{{Kind: "critique", Reference: "review/knowledge-critique"}}}
	todo.Content = &content
	admitted, err := store.AdmitTODO(ctx, "company-knowledge", "builder", TODOAdmissionRequest{SchemaVersion: todoCreationSchemaVersion, OperationID: "knowledge-admit", TODO: todo})
	if err != nil {
		t.Fatalf("admit Knowledge TODO: %v", err)
	}
	current, err := store.ReadTODO(ctx, "company-knowledge", "builder", todo.ID, nil)
	if err != nil || current.ContentDigest != admitted.ContentDigest {
		t.Fatalf("read admitted TODO: %+v %v", current, err)
	}
	reviewRefs := []TODOProofReference{{Kind: "planning_review_critique", Source: "human-review", Reference: "review/knowledge-critique", SourceDigest: current.ContentDigest, VerificationState: "unverified", Outcome: "go"}}
	if _, err := store.EditTODOEvidence(ctx, "company-knowledge", TODOEvidenceEditRequest{ProjectID: "builder", TODOID: todo.ID, ExpectedRevision: &current.TODORevision, OperationID: "knowledge-review", ProofReferences: reviewRefs, WaiverReferences: []TODOProofReference{}, ImplementationCommits: []TODOCommitReference{}, RecordSections: recordRecordSections(current.TODO), Reason: "Record reviewed Knowledge delivery contract"}); err != nil {
		t.Fatalf("review evidence: %v", err)
	}
	current, err = store.ReadTODO(ctx, "company-knowledge", "builder", todo.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	advance := func(operation, stage string, refs []TODOProofReference, delivery *TODOKnowledgeDeliveryClaim, acceptance *TODOKnowledgeFinalAcceptance) TODOReadResponse {
		t.Helper()
		request := TODOActionRequest{ProjectID: "builder", TODOID: todo.ID, ExpectedRevision: &current.TODORevision, OperationID: operation, RequestedStage: stage, ProofReferences: refs, KnowledgeDelivery: delivery, KnowledgeFinalAcceptance: acceptance}
		if _, err := store.TransitionTODO(ctx, "company-knowledge", request); err != nil {
			t.Fatalf("%s transition: %v", stage, err)
		}
		read, err := store.ReadTODO(ctx, "company-knowledge", "builder", todo.ID, nil)
		if err != nil || read.TODO.Stage != stage || read.TODO.StageVerification != "verified" {
			t.Fatalf("%s readback: %+v %v", stage, read, err)
		}
		current = read
		return read
	}
	advance("knowledge-review-stage", "Implementation-Approval-Pending", nil, nil, nil)
	approval := TODOProofReference{Kind: "owner-approval", Source: "owner-chat", Reference: "APROVADO", SourceDigest: current.ContentDigest, VerificationState: "unverified"}
	advance("knowledge-ready", "Ready-for-Implementation", []TODOProofReference{approval}, nil, nil)
	historicalClaim := &TODOKnowledgeDeliveryClaim{ContentDigest: current.ContentDigest, Items: []TODOKnowledgeDeliveryItem{{Family: FamilyModule, RecordID: "projects", GenerationID: "g1", ToRevision: delivered.Revision, ToDigest: delivered.ContentDigest, CriterionIDs: []string{"criterion-one"}, ScenarioIDs: []string{"scenario-one"}}}, OwnerAttributionReference: "owner confirms this preapproval Knowledge edit", Retrospective: true}
	advance("knowledge-late-work", "In-Progress", nil, historicalClaim, nil)
	if current.TODO.VerifiedTransitionRef == nil || current.TODO.VerifiedTransitionRef.Kind != "late-work-verification" || current.TODO.KnowledgeDeliveryProof == nil {
		t.Fatalf("retrospective proof missing: %+v", current.TODO)
	}
	deliveryRefs := append(append([]TODOProofReference{}, reviewRefs...), TODOProofReference{Kind: "final_review", Source: "human-review", Reference: "review/knowledge-final", SourceDigest: current.ContentDigest, VerificationState: "unverified", Outcome: "go"})
	if _, err := store.EditTODOEvidence(ctx, "company-knowledge", TODOEvidenceEditRequest{ProjectID: "builder", TODOID: todo.ID, ExpectedRevision: &current.TODORevision, OperationID: "knowledge-final-review", ProofReferences: deliveryRefs, WaiverReferences: []TODOProofReference{}, ImplementationCommits: []TODOCommitReference{}, RecordSections: recordRecordSections(current.TODO), Reason: "Record fixture final review"}); err != nil {
		t.Fatalf("final review evidence: %v", err)
	}
	current, err = store.ReadTODO(ctx, "company-knowledge", "builder", todo.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	local := advance("knowledge-local", "Local-Implemented", nil, nil, nil)
	if local.TODO.KnowledgeDeliveryProof == nil || local.TODO.KnowledgeDeliveryProof.SetDigest == "" {
		t.Fatal("Local lost typed Knowledge proof")
	}
	localAsOf, err := store.ReadTODOAsOf(ctx, "company-knowledge", "builder", todo.ID, local.TODORevision)
	if err != nil || localAsOf.State.KnowledgeDeliveryProof == nil || localAsOf.State.KnowledgeDeliveryProof.SetDigest != local.TODO.KnowledgeDeliveryProof.SetDigest {
		t.Fatalf("Local history proof mismatch: %+v %v", localAsOf, err)
	}
	final := &TODOKnowledgeFinalAcceptance{ContentDigest: current.ContentDigest, SetDigest: local.TODO.KnowledgeDeliveryProof.SetDigest, OwnerReference: "owner/final-acceptance", Reason: "Documentation accepted"}
	for name, acceptance := range map[string]*TODOKnowledgeFinalAcceptance{
		"missing":             nil,
		"stale content":       {ContentDigest: "sha256:" + strings.Repeat("f", 64), SetDigest: final.SetDigest, OwnerReference: final.OwnerReference, Reason: final.Reason},
		"wrong delivered set": {ContentDigest: final.ContentDigest, SetDigest: "sha256:" + strings.Repeat("f", 64), OwnerReference: final.OwnerReference, Reason: final.Reason},
	} {
		t.Run("completed rejects "+name+" acceptance", func(t *testing.T) {
			request := TODOActionRequest{ProjectID: "builder", TODOID: todo.ID, ExpectedRevision: &current.TODORevision, OperationID: "knowledge-reject-" + strings.ReplaceAll(name, " ", "-"), RequestedStage: "Completed", KnowledgeFinalAcceptance: acceptance}
			if _, err := store.TransitionTODO(ctx, "company-knowledge", request); err == nil {
				t.Fatal("Completed accepted missing or mismatched Knowledge owner acceptance")
			}
			unchanged, err := store.ReadTODO(ctx, "company-knowledge", "builder", todo.ID, nil)
			if err != nil || unchanged.TODORevision != current.TODORevision || unchanged.TODO.Stage != "Local-Implemented" {
				t.Fatalf("rejected Completed attempt changed TODO state: %+v %v", unchanged, err)
			}
		})
	}
	for name, mutate := range map[string]func(*TODOActionRequest){
		"code-run substitution": func(request *TODOActionRequest) { request.LocalProof = &LocalProofResult{} },
		"deployment substitution": func(request *TODOActionRequest) {
			request.ProofReferences = []TODOProofReference{{Kind: "release-publication", Source: "github", Reference: "deployment/123", SourceDigest: current.ContentDigest}}
		},
	} {
		t.Run("completed rejects "+name, func(t *testing.T) {
			request := TODOActionRequest{ProjectID: "builder", TODOID: todo.ID, ExpectedRevision: &current.TODORevision, OperationID: "knowledge-reject-" + strings.ReplaceAll(name, " ", "-"), RequestedStage: "Completed", KnowledgeFinalAcceptance: final}
			mutate(&request)
			if _, err := store.TransitionTODO(ctx, "company-knowledge", request); err == nil {
				t.Fatal("Completed accepted non-Knowledge proof instead of owner acceptance")
			}
			unchanged, err := store.ReadTODO(ctx, "company-knowledge", "builder", todo.ID, nil)
			if err != nil || unchanged.TODORevision != current.TODORevision || unchanged.TODO.Stage != "Local-Implemented" {
				t.Fatalf("rejected substitution changed TODO state: %+v %v", unchanged, err)
			}
		})
	}
	completed := advance("knowledge-completed", "Completed", nil, nil, final)
	if completed.TODO.VerifiedTransitionRef == nil || completed.TODO.VerifiedTransitionRef.Kind != "knowledge-final-acceptance" {
		t.Fatalf("Completed missing distinct documentary proof: %+v", completed.TODO)
	}
	completedAsOf, err := store.ReadTODOAsOf(ctx, "company-knowledge", "builder", todo.ID, completed.TODORevision)
	if err != nil || completedAsOf.State.Stage != "Completed" || completedAsOf.State.VerifiedTransitionRef == nil || completedAsOf.State.VerifiedTransitionRef.Kind != "knowledge-final-acceptance" || completedAsOf.State.KnowledgeDeliveryProof == nil || completedAsOf.State.KnowledgeDeliveryProof.SetDigest != local.TODO.KnowledgeDeliveryProof.SetDigest {
		t.Fatalf("Completed history lacks exact documentary acceptance: %+v %v", completedAsOf, err)
	}

	// A separate TODO proves the ordinary prospective route with an actual
	// work-started checkpoint followed by an API-born Knowledge creation.
	prospective := mongoBornTODOFixture()
	prospective.ID, prospective.ReleaseID, prospective.Position = "prospective-knowledge-todo", "r-knowledge", intPointer(1)
	prospectiveContent := validTODOContent()
	prospectiveContent.Delivery = &TODODeliveryContract{Kind: "knowledge-only", Targets: []TODOKnowledgeTarget{{Family: FamilyModule, RecordID: "new-module", GenerationID: "g1", BaselineRevision: 0, ChangeKind: "create"}}}
	prospectiveContent.Planning = &TODOPlanning{Complexity: "small", AuditTriggers: testAuditTriggers(), Assumptions: []TODOPlanningAssumption{}, Reviews: []TODOPlanningReview{{Kind: "critique", Reference: "review/prospective-critique"}}}
	prospective.Content = &prospectiveContent
	if _, err := store.AdmitTODO(ctx, "company-knowledge", "builder", TODOAdmissionRequest{SchemaVersion: todoCreationSchemaVersion, OperationID: "prospective-admit", TODO: prospective}); err != nil {
		t.Fatalf("admit prospective TODO: %v", err)
	}
	todo = prospective
	current, err = store.ReadTODO(ctx, "company-knowledge", "builder", todo.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	prospectiveReviewRefs := []TODOProofReference{{Kind: "planning_review_critique", Source: "human-review", Reference: "review/prospective-critique", SourceDigest: current.ContentDigest, VerificationState: "unverified", Outcome: "go"}}
	if _, err := store.EditTODOEvidence(ctx, "company-knowledge", TODOEvidenceEditRequest{ProjectID: "builder", TODOID: todo.ID, ExpectedRevision: &current.TODORevision, OperationID: "prospective-review", ProofReferences: prospectiveReviewRefs, WaiverReferences: []TODOProofReference{}, ImplementationCommits: []TODOCommitReference{}, RecordSections: recordRecordSections(current.TODO), Reason: "Record prospective review"}); err != nil {
		t.Fatalf("record prospective review: %v", err)
	}
	current, err = store.ReadTODO(ctx, "company-knowledge", "builder", todo.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	advance("prospective-pending", "Implementation-Approval-Pending", nil, nil, nil)
	approval.SourceDigest = current.ContentDigest
	advance("prospective-ready", "Ready-for-Implementation", []TODOProofReference{approval}, nil, nil)
	advance("prospective-start", "In-Progress", nil, nil, nil)
	if current.TODO.VerifiedTransitionRef == nil || current.TODO.VerifiedTransitionRef.Kind != "work-started" {
		t.Fatal("prospective path did not record work-started")
	}
	time.Sleep(5 * time.Millisecond)
	created, err := store.CreateCanonical(ctx, "company-knowledge", CanonicalMutation{ProjectID: "builder", Family: FamilyModule, Record: canonicalTestRecord(FamilyModule, "new-module", `{"purpose":"New documented module","decisions":[],"schemas":[]}`).Record, ExpectedRevision: 0, OperationID: "create-new-module", Actor: "owner"})
	if err != nil {
		t.Fatalf("create Knowledge after work-started: %v", err)
	}
	prospectiveDeliveryRefs := append(append([]TODOProofReference{}, prospectiveReviewRefs...), TODOProofReference{Kind: "final_review", Source: "human-review", Reference: "review/prospective-final", SourceDigest: current.ContentDigest, VerificationState: "unverified", Outcome: "go"})
	if _, err := store.EditTODOEvidence(ctx, "company-knowledge", TODOEvidenceEditRequest{ProjectID: "builder", TODOID: todo.ID, ExpectedRevision: &current.TODORevision, OperationID: "prospective-final-review", ProofReferences: prospectiveDeliveryRefs, WaiverReferences: []TODOProofReference{}, ImplementationCommits: []TODOCommitReference{}, RecordSections: recordRecordSections(current.TODO), Reason: "Record prospective final review"}); err != nil {
		t.Fatalf("record prospective final review: %v", err)
	}
	current, err = store.ReadTODO(ctx, "company-knowledge", "builder", todo.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	createClaim := &TODOKnowledgeDeliveryClaim{ContentDigest: current.ContentDigest, OwnerAttributionReference: "owner attributes new module", Items: []TODOKnowledgeDeliveryItem{{Family: FamilyModule, RecordID: "new-module", GenerationID: "g1", ToRevision: created.Revision, ToDigest: created.ContentDigest, CriterionIDs: []string{"criterion-one"}, ScenarioIDs: []string{"scenario-one"}}}}
	prospectiveLocal := advance("prospective-local", "Local-Implemented", nil, createClaim, nil)
	if prospectiveLocal.TODO.KnowledgeDeliveryProof == nil || prospectiveLocal.TODO.KnowledgeDeliveryProof.Retrospective {
		t.Fatal("prospective Knowledge delivery was not verified")
	}

	// The same exact immutable verifier accepts deletion and restoration only
	// when the approved kind and delivered deletion state match the event.
	deleteTarget := TODOKnowledgeTarget{Family: FamilyModule, RecordID: "new-module", GenerationID: "g1", BaselineRevision: created.Revision, BaselineDigest: created.ContentDigest, ChangeKind: "delete"}
	verifyRecord := TODORecord{ID: "kind-proof", ProjectID: "builder", Content: &TODOContent{Delivery: &TODODeliveryContract{Kind: "knowledge-only", Targets: []TODOKnowledgeTarget{deleteTarget}}, Criteria: []TODOContentItem{{ID: "criterion-one"}}, ValidationScenarios: []TODOValidationScenario{{ID: "scenario-one", CriterionIDs: []string{"criterion-one"}}}}}
	time.Sleep(5 * time.Millisecond)
	deleted, err := store.DeleteCanonical(ctx, "company-knowledge", CanonicalDeleteRequest{ProjectID: "builder", Family: FamilyModule, RecordID: "new-module", ExpectedRevision: created.Revision, OperationID: "delete-new-module", Actor: "owner"})
	if err != nil {
		t.Fatalf("delete Knowledge record: %v", err)
	}
	kindClaim := &TODOKnowledgeDeliveryClaim{ContentDigest: contentDigest, OwnerAttributionReference: "owner confirms deletion", Items: []TODOKnowledgeDeliveryItem{{Family: FamilyModule, RecordID: "new-module", GenerationID: "g1", ToRevision: deleted.Revision, ToDigest: deleted.ContentDigest, CriterionIDs: []string{"criterion-one"}, ScenarioIDs: []string{"scenario-one"}}}}
	if _, err := store.verifyKnowledgeDelivery(ctx, "company-knowledge", project, verifyRecord, contentDigest, kindClaim, false, created.UpdatedAt, created.UpdatedAt); err != nil {
		t.Fatalf("exact deletion proof rejected: %v", err)
	}
	verifyRecord.Content.Delivery.Targets[0] = TODOKnowledgeTarget{Family: FamilyModule, RecordID: "new-module", GenerationID: "g1", BaselineRevision: deleted.Revision, BaselineDigest: deleted.ContentDigest, ChangeKind: "restore"}
	time.Sleep(5 * time.Millisecond)
	restored, err := store.RestoreCanonical(ctx, "company-knowledge", CanonicalRestoreRequest{ProjectID: "builder", Family: FamilyModule, RecordID: "new-module", Revision: created.Revision, ExpectedRevision: deleted.Revision, OperationID: "restore-new-module", Actor: "owner"})
	if err != nil {
		t.Fatalf("restore Knowledge record: %v", err)
	}
	kindClaim.Items[0].ToRevision, kindClaim.Items[0].ToDigest = restored.Revision, restored.ContentDigest
	if _, err := store.verifyKnowledgeDelivery(ctx, "company-knowledge", project, verifyRecord, contentDigest, kindClaim, false, deleted.UpdatedAt, deleted.UpdatedAt); err != nil {
		t.Fatalf("exact restoration proof rejected: %v", err)
	}
}
