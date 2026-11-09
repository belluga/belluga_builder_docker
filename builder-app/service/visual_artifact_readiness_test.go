package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// This opt-in test checks the selected, published artifacts through the real
// Builder HTTP boundary. Unit fixtures cannot prove that a Project's current
// source revision is admitted or that its rendered files remain reachable.
func TestSelectedProjectVisualArtifactReadiness(t *testing.T) {
	base := strings.TrimRight(os.Getenv("BUILDER_VISUAL_READINESS_BASE_URL"), "/")
	company := os.Getenv("BUILDER_VISUAL_READINESS_COMPANY_ID")
	project := os.Getenv("BUILDER_VISUAL_READINESS_PROJECT_ID")
	prototypeSHA := os.Getenv("BUILDER_VISUAL_READINESS_PROTOTYPE_SHA")
	designSystemSHA := os.Getenv("BUILDER_VISUAL_READINESS_DESIGN_SYSTEM_SHA")
	if base == "" && company == "" && project == "" {
		t.Skip("set BUILDER_VISUAL_READINESS_BASE_URL, COMPANY_ID and PROJECT_ID for the real-API check")
	}
	if base == "" || company == "" || project == "" || prototypeSHA == "" || designSystemSHA == "" {
		t.Fatal("the real-API check requires BASE_URL, COMPANY_ID, PROJECT_ID, PROTOTYPE_SHA and DESIGN_SYSTEM_SHA")
	}
	if _, err := url.ParseRequestURI(base); err != nil {
		t.Fatalf("invalid Builder base URL: %v", err)
	}
	minimum := 1
	if raw := os.Getenv("BUILDER_VISUAL_READINESS_MIN_PROTOTYPES"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 {
			t.Fatalf("invalid minimum Prototype count %q", raw)
		}
		minimum = value
	}
	client := &http.Client{Timeout: 15 * time.Second}
	query := url.Values{"company_id": {company}, "project_id": {project}}.Encode()
	expectedPrototypeIDs := strings.Split(os.Getenv("BUILDER_VISUAL_READINESS_PROTOTYPE_IDS"), ",")
	expectedDesignSystemID := os.Getenv("BUILDER_VISUAL_READINESS_DESIGN_SYSTEM_ID")
	if expectedPrototypeIDs[0] == "" || expectedDesignSystemID == "" {
		t.Fatal("the real-API check requires explicit Prototype and Design System IDs")
	}

	t.Run("published Prototype screens", func(t *testing.T) {
		var result struct {
			ConsumerReadiness    string `json:"consumer_readiness"`
			ProjectSHA           string `json:"project_sha"`
			SnapshotFingerprint  string `json:"snapshot_fingerprint"`
			SnapshotObservations struct {
				PrototypeStatus struct {
					Outcome  string `json:"outcome"`
					Revision string `json:"revision"`
					Items    []struct {
						ID         string `json:"id"`
						Root       string `json:"root"`
						EntryPoint string `json:"entry_point"`
						Screens    []struct {
							Path string `json:"path"`
						} `json:"screens"`
					} `json:"items"`
					Diagnostics []struct {
						Code    string `json:"code"`
						Path    string `json:"path"`
						Message string `json:"message"`
					} `json:"diagnostics"`
				} `json:"prototype_status"`
			} `json:"snapshot_observations"`
		}
		readVisualJSON(t, client, base+"/api/local-preview?"+query, &result)
		status := result.SnapshotObservations.PrototypeStatus
		if result.ConsumerReadiness != "ready" {
			t.Errorf("local preview consumer readiness = %q, want ready", result.ConsumerReadiness)
		}
		if result.ProjectSHA != prototypeSHA || status.Revision != prototypeSHA {
			t.Errorf("Prototype source = project %q/status %q, want %q", result.ProjectSHA, status.Revision, prototypeSHA)
		}
		if status.Outcome != "go" || len(status.Items) < minimum {
			t.Fatalf("selected Prototype catalog outcome=%q items=%d, want go with at least %d; diagnostics=%+v", status.Outcome, len(status.Items), minimum, status.Diagnostics)
		}
		actualIDs := make(map[string]bool, len(status.Items))
		for _, item := range status.Items {
			actualIDs[item.ID] = true
		}
		for _, expectedID := range expectedPrototypeIDs {
			if expectedID == "" || !actualIDs[expectedID] {
				t.Errorf("expected Prototype %q is absent from the admitted selection", expectedID)
			}
		}
		for _, item := range status.Items {
			if item.Root == "" || item.EntryPoint == "" || len(item.Screens) == 0 || result.SnapshotFingerprint == "" {
				t.Errorf("Prototype %q lacks a published entry point, screens or snapshot identity", item.ID)
				continue
			}
			entryDeclared := false
			for _, screen := range item.Screens {
				entryDeclared = entryDeclared || screen.Path == item.EntryPoint
				path := fmt.Sprintf("/snapshot/%s/%s/%s/%s/%s", url.PathEscape(company), url.PathEscape(project), url.PathEscape(result.SnapshotFingerprint), item.Root, screen.Path)
				readVisualHTML(t, client, base+path)
			}
			if !entryDeclared {
				t.Errorf("Prototype %q entry point %q is absent from its selected screen inventory", item.ID, item.EntryPoint)
			}
		}
	})

	t.Run("selected Design System presentation", func(t *testing.T) {
		var result struct {
			Selection struct {
				SHA string `json:"sha"`
			} `json:"selection"`
			Evaluation struct {
				Outcome      string `json:"outcome"`
				Structural   string `json:"design_system_validation"`
				Presentation string `json:"presentation_validation"`
				Teach        *struct {
					Code       string `json:"code"`
					Resolution string `json:"resolution"`
				} `json:"presentation_teach"`
				Items []struct {
					ID         string `json:"id"`
					Components []struct {
						Examples []string `json:"examples"`
					} `json:"components"`
				} `json:"items"`
			} `json:"evaluation"`
		}
		readVisualJSON(t, client, base+"/api/design-system?"+query, &result)
		if result.Evaluation.Outcome != "go" || result.Evaluation.Structural != "valid" {
			t.Errorf("selected Design System structural result = %q/%q, want go/valid", result.Evaluation.Outcome, result.Evaluation.Structural)
		}
		if result.Evaluation.Presentation != "ready" || result.Evaluation.Teach != nil {
			t.Errorf("selected Design System presentation = %q, want ready; TEACH=%+v", result.Evaluation.Presentation, result.Evaluation.Teach)
		}
		if expectedDesignSystemID != "" && (len(result.Evaluation.Items) != 1 || result.Evaluation.Items[0].ID != expectedDesignSystemID) {
			t.Errorf("selected Design System identity does not match %q: %+v", expectedDesignSystemID, result.Evaluation.Items)
		}
		if result.Selection.SHA == "" {
			t.Fatal("selected Design System has no published revision")
		}
		if result.Selection.SHA != designSystemSHA {
			t.Errorf("selected Design System revision = %q, want %q", result.Selection.SHA, designSystemSHA)
		}
		for _, item := range result.Evaluation.Items {
			for _, component := range item.Components {
				for _, example := range component.Examples {
					if strings.HasSuffix(example, ".html") {
						path := fmt.Sprintf("/api/design-system/presentation/%s/%s/%s/%s", url.PathEscape(company), url.PathEscape(project), url.PathEscape(result.Selection.SHA), example)
						readVisualHTML(t, client, base+path)
						return
					}
				}
			}
		}
		t.Error("selected Design System has no declared HTML presentation example")
	})
}

func readVisualJSON(t *testing.T, client *http.Client, address string, target any) {
	t.Helper()
	response, err := client.Get(address)
	if err != nil {
		t.Fatalf("read %s: %v", address, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("read %s: HTTP %d", address, response.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(target); err != nil {
		t.Fatalf("decode %s: %v", address, err)
	}
}

func readVisualHTML(t *testing.T, client *http.Client, address string) {
	t.Helper()
	response, err := client.Get(address)
	if err != nil {
		t.Errorf("open published HTML %s: %v", address, err)
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Errorf("open published HTML %s: HTTP %d", address, response.StatusCode)
		return
	}
	if !strings.Contains(response.Header.Get("Content-Type"), "text/html") {
		t.Errorf("published HTML %s has content type %q", address, response.Header.Get("Content-Type"))
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if err != nil || !strings.Contains(strings.ToLower(string(body)), "<html") {
		t.Errorf("published HTML %s has no readable HTML document: %v", address, err)
	}
}
