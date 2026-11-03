package registry

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestCanonicalCorpusContracts(t *testing.T) {
	cases := []struct {
		name    string
		family  CanonicalFamily
		payload string
	}{
		{"builder-promotion", FamilyConstitution, `{"promotion_mode":"mvp","capability_namespaces":["docker","go","react","vite"],"rules":[{"id":"authority","status":"current","statement":"Project API owns active canon"}]}`},
		{"boora-promotion", FamilyConstitution, `{"promotion_mode":"complete","capability_namespaces":["docker"],"rules":[{"id":"authority","status":"current","statement":"Project owns promotion"}]}`},
		{"builder-mandate-sections", FamilyMandate, `{"purpose":"Builder purpose","working_name":"Builder","working_name_state":"provisional","working_name_policy":"Branding remains deferred; IDs do not migrate with the name","business_thesis":"Enterprise thesis","desired_outcomes":["Visible work"],"scope_context":"This scope is current","scope":["Projects"],"principles":["Deterministic guards"],"exclusions":["No AI inference"],"future_boundary":"Future IAM","success_measures_context":"Measures are observational","success_measures":["Time to first read"]}`},
		{"builder-domain-vocabulary", FamilyDomain, `{"entities":[{"id":"project","name":"Project","description":"Collaboration boundary","authority_boundary":"Owns artifacts"},{"id":"release","name":"Release","description":"Groups work"}],"relations":[{"id":"mig-1234567890abcdef","from":"project","to":"release","kind":"groups","statement":"A Project has Releases","cardinality":"zero or many"}],"relationship_rules":[{"id":"mig-relationship-rule","statement":"A Company can own multiple Projects, Epics and Releases through their declared ownership chain"}],"distinctions":[{"id":"mig-234567890abcdef1","terms":["Epic","Release","TODO"],"reason":"Strategy, grouping and execution differ"}],"qa_validation_outline":{"applicability":"target only","fields":[{"name":"target_revision","text":"Revision under test"}],"definitions":[{"name":"approved","text":"QA acceptance"}]},"lifecycle_boundary":"No lifecycle enums defined here"}`},
		{"builder-constitution-boundaries", FamilyConstitution, `{"promotion_mode":"mvp","capability_namespaces":["go"],"authority_boundaries":[{"name":"Project knowledge","text":"API after activation"}],"control_plane_responsibilities":[{"name":"Reader","text":"Scoped read"}],"publication_flow":[{"name":"Governed publication","steps":[{"name":"Authorize","text":"Check grant"},{"name":"Publish","text":"Record revision"}]}],"synchronization_outcomes":[{"name":"ready","text":"Current"}],"freshness_states":[{"name":"mismatch","text":"Source changed"}],"module_boundary_refs":[{"kind":"module","ref":"design-artifact-authoring"}],"rules":[{"id":"mig-345678901abcdef2","statement":"Do not infer authority"}],"maintenance_rule":"Update when policy changes"}`},
		{"builder-roadmap-distinction", FamilyRoadmap, `{"strategic_objective":"Build governed work","epic_sequence_context":"Candidate Epics indicate direction, not admitted work","epic_candidates":[{"name":"Local harness","text":"First strategic direction"}],"stages":[{"id":"stage-1","name":"Foundation","purpose":"Establish authority","context":"First local milestone","executive_value":"Stakeholders see progress","first_viewer_slice":"Read-only project view","open_fronts":["Knowledge"],"capabilities":["Documents"]}],"deferred_options":[{"name":"Provider AI","text":"Deferred"}],"admission_rule":"Require governed plan"}`},
		{"builder-policy-scope", FamilyPolicy, `{"purpose":"Prevent inferred scopes","scope_concepts":[{"name":"Project","text":"Top-level boundary"}],"prohibited_inferences":["Do not infer tenant from path"],"future_surface_condition":"Before a route, screen, MCP operation or API resource is implemented","future_surface_declarations":[{"name":"owning module","text":"Declare before route"}],"environment_type_state":"undefined","maintenance_rule":"Update before new scope","rules":[{"id":"mig-45678901abcdef23","statement":"No implicit scope"}],"ownership":[]}`},
		{"builder-module-coverage-and-union", FamilyModule, `{"purpose":"Own design artifacts","coverage":[{"name":"current","text":"Prototype v3 supported"}],"processes":[{"name":"Publication","steps":[{"name":"Validate","text":"Run structural guard"},{"name":"Read","text":"Open admitted artifact"}]}],"schemas":[{"id":"prototype","name":"Prototype","kind":"artifact","fields":[{"name":"scope","type":"null"},{"name":"related","type":"union","one_of":[{"name":"related","type":"object","fields":[{"name":"path","type":"string"}]},{"name":"related","type":"object","fields":[{"name":"document_id","type":"string"}]}]}]}]}`},
		{"module-without-decisions", FamilyModule, `{"purpose":"Own map POIs","scope":["tenant runtime"],"exclusions":["attendance"],"routes":[{"route":"/mapa","host_scope":"tenant","environment_type":"tenant","main_scope":"tenant_public","subscope":null,"guard":"public session"}]}`},
		{"invite-schema", FamilyModule, `{"purpose":"Own social decisions","schemas":[{"id":"invite-edges","name":"invite_edges","kind":"collection","fields":[{"name":"inviter_principal","type":"object","required":true,"fields":[{"name":"kind","type":"string","required":true,"enum":["user","account_profile"]},{"name":"id","type":"object_id","required":true}]},{"name":"contextual_prompts","type":"array","items":{"name":"prompt","type":"object","fields":[{"name":"cta","type":"string"}]}},{"name":"account_profile_id","type":"object_id","nullable":true}],"indexes":[{"id":"recipient-upsert","keys":[{"field":"owner_user_id","order":"asc"},{"field":"receiver_account_profile_id","order":"asc"}],"unique":true}],"invariants":[{"id":"profile-principal","status":"current","statement":"Profile principal must match issuing profile","applies_to":["inviter_principal.kind","account_profile_id"]}]}]}`},
		{"local-cli", FamilyModule, `{"purpose":"Validate artifacts","local_operations":[{"id":"artifact-status","command":"delphi-ai/local-api/run artifact-status prototypes","flags":[{"name":"--project-id","type":"string","required":true},{"name":"--mode","type":"string","enum":["working-tree","committed"]}],"exit_codes":[{"code":0,"outcome":"go","stdout":"complete JSON","consumer_action":"consume invocation stdout"},{"code":64,"outcome":"usage-error","stderr":"sanitized usage","consumer_action":"correct flags"}],"digest":{"algorithm":"sha256","fields":["sources","assets"],"exclusions":["landing"]}}],"decisions":[{"id":"DEC-ART-01","status":"current","semantic_status":"Approved architecture","statement":"Structural evaluation remains separate","rationale":"No implicit publication","evidence":[{"kind":"module","ref":"design-artifact-authoring"}]}]}`},
		{"events-and-jobs", FamilyModule, `{"purpose":"Client lifecycle","events":[{"name":"app.session_bootstrapped","direction":"outbound","transport":"analytics","payload":[{"name":"user_id","type":"string"}]}],"jobs":[{"id":"daily-refresh","handler":"DailyRefreshJob","schedule":"07:00 daily","timezone":"local","lifecycle_requirements":["pause on app suspend"]}],"observability":[{"id":"crash-free","kind":"alert","name":"crash-free sessions","comparison":"gte","threshold":99,"unit":"percent","fields":[{"name":"controller","type":"string"}]}],"verification":[{"id":"compatibility","layer":"integration","requirement":"Real Flutter-Laravel invite compatibility","commands":["project-owned runner"]}],"open_questions":[{"id":"native-admin","question":"How does native admin rebind?","status":"open"}]}`},
		{"endpoint-contract", FamilyEndpoint, `{"method":"PATCH","route":"/api/v1/account_profiles/{id}","host_scope":"tenant","module_id":"accounts","request":[{"name":"description","type":"string","nullable":true,"location":"body"}],"response":[{"name":"data","type":"object","fields":[{"name":"id","type":"object_id"}]}],"security":{"level":"L3","authentication":"bearer","abilities":["profiles:update"],"trusted_proxy_required":true,"rejection_codes":["rate_limited"],"response_headers":["X-Correlation-Id"]},"idempotency":{"required":true,"header":"Idempotency-Key","replay_window_seconds":300},"patch":{"omitted_fields":"unchanged","null_fields":"clear_nullable","atomic":true},"responses":[{"status_code":422,"description":"Field validation","fields":[{"name":"errors","type":"object"}]}]}`},
		{"endpoint-conventions-policy", FamilyPolicy, `{"rules":[{"id":"security","status":"current","statement":"Edge and application controls compose"}],"ownership":[],"endpoint_conventions":{"base_path":"/api/v1","id_format":"object_id_string","datetime_format":"RFC3339","tenant_id_required":true,"pagination":{"mode":"page","request_fields":[{"name":"page","type":"integer"}],"response_fields":[{"name":"has_more","type":"boolean"}]},"patch":{"omitted_fields":"unchanged","null_fields":"clear_nullable","atomic":true}}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			record := canonicalTestRecord(tc.family, "corpus", tc.payload).Record
			if err := validateCanonicalRecord("builder", "g1", tc.family, record); err != nil {
				t.Fatal(err)
			}
			stored := canonicalStoredRecord(tc.family, record).toRecord()
			if canonicalDigest(stored.Payload) != canonicalDigest(record.Payload) {
				t.Fatal("typed corpus contract changed during BSON/API payload conversion")
			}
			if tc.family == FamilyConstitution {
				var value ConstitutionPayload
				if err := json.Unmarshal(record.Payload, &value); err != nil {
					t.Fatal(err)
				}
				encoded, _ := json.Marshal(value)
				if !strings.Contains(string(encoded), `"promotion_mode":`) {
					t.Fatal("promotion mode lost in API representation")
				}
			}
		})
	}
}

func TestCanonicalSchemaRejectsLossAndDecisionCollisions(t *testing.T) {
	cases := []struct {
		name                  string
		family                CanonicalFamily
		payload, path, action string
	}{
		{"duplicate-PCO-12", FamilyModule, `{"purpose":"Profiles","decisions":[{"id":"PCO-12","status":"current","statement":"Hero fallback"},{"id":"PCO-12","status":"current","statement":"Taxonomy snapshot"}]}`, "payload.decisions[1].id", "PCO-12"},
		{"promotion-required", FamilyConstitution, `{"rules":[{"id":"rule","status":"current","statement":"Rule"}]}`, "payload.promotion_mode", "mvp"},
		{"promotion-invalid", FamilyConstitution, `{"promotion_mode":"dev","capability_namespaces":["go"],"rules":[{"id":"rule","status":"current","statement":"Rule"}]}`, "payload.promotion_mode", "complete"},
		{"nested-invalid-type", FamilyModule, `{"purpose":"Nested","schemas":[{"id":"nested","name":"nested","kind":"collection","fields":[{"name":"item","type":"object","fields":[{"name":"x","type":"whatever"}]}]}]}`, "payload.schemas[0].fields[0].fields[0].type", "string"},
		{"duplicate-field", FamilyEndpoint, `{"method":"GET","route":"/x","host_scope":"tenant","module_id":"maps","request":[{"name":"page","type":"integer"},{"name":"page","type":"integer"}],"response":[]}`, "payload.request[1].name", "duplicate"},
		{"unknown-field", FamilyModule, `{"purpose":"No untyped sections","sections":[{"text":"hidden contract"}]}`, "payload.sections", "unknown"},
		{"trailing-value", FamilyModule, `{"purpose":"Only one object"} {}`, "payload", "one JSON"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateCanonicalRecord("builder", "g1", tc.family, canonicalTestRecord(tc.family, "corpus", tc.payload).Record)
			var teach *canonicalTeachError
			if !errors.As(err, &teach) || teach.Diagnostic.FieldPath != tc.path || !strings.Contains(teach.Diagnostic.NextAction, tc.action) {
				t.Fatalf("expected %s and %q; got %v", tc.path, tc.action, err)
			}
		})
	}
}
