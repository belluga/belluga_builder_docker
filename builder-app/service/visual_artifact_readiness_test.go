package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
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
	expectedInventoryDigest := os.Getenv("BUILDER_VISUAL_READINESS_INVENTORY_DIGEST")
	if expectedPrototypeIDs[0] == "" || expectedDesignSystemID == "" || expectedInventoryDigest == "" {
		t.Fatal("the real-API check requires explicit Prototype IDs, Design System ID and Screen inventory digest")
	}

	t.Run("published Prototype screens", func(t *testing.T) {
		var result struct {
			Revision        string `json:"revision"`
			InventoryDigest string `json:"inventory_digest"`
			Items           []struct {
				ID        string `json:"id"`
				Root      string `json:"root"`
				DetailURL string `json:"detail_url"`
			} `json:"items"`
		}
		readVisualJSON(t, client, base+"/api/prototypes?"+query, &result)
		if result.Revision != prototypeSHA {
			t.Errorf("Prototype source revision = %q, want %q", result.Revision, prototypeSHA)
		}
		if !strings.HasPrefix(result.InventoryDigest, "sha256:") {
			t.Errorf("Prototype catalog has invalid inventory digest %q", result.InventoryDigest)
		}
		if len(result.Items) < minimum {
			t.Fatalf("selected Prototype catalog has %d items, want at least %d", len(result.Items), minimum)
		}
		actualIDs := make(map[string]bool, len(result.Items))
		inventory := make([]string, 0)
		for _, item := range result.Items {
			actualIDs[item.ID] = true
		}
		for _, expectedID := range expectedPrototypeIDs {
			if expectedID == "" || !actualIDs[expectedID] {
				t.Errorf("expected Prototype %q is absent from the admitted selection", expectedID)
			}
		}
		for _, item := range result.Items {
			if item.Root == "" || item.DetailURL == "" {
				t.Errorf("Prototype %q lacks its canonical detail URL or root", item.ID)
				continue
			}
			var detail struct {
				Revision        string `json:"revision"`
				InventoryDigest string `json:"inventory_digest"`
				Item            struct {
					ID         string `json:"id"`
					Root       string `json:"root"`
					EntryPoint string `json:"entry_point"`
					Screens    []struct {
						Path string `json:"path"`
					} `json:"screens"`
				} `json:"item"`
				FileURLs []struct {
					Path string `json:"path"`
					URL  string `json:"url"`
				} `json:"file_urls"`
			}
			readVisualJSON(t, client, base+item.DetailURL, &detail)
			if detail.Revision != prototypeSHA || detail.InventoryDigest != result.InventoryDigest || detail.Item.ID != item.ID || detail.Item.Root != item.Root || detail.Item.EntryPoint == "" || len(detail.Item.Screens) == 0 {
				t.Errorf("Prototype %q detail does not match canonical catalog identity: revision=%q digest=%q item=%+v", item.ID, detail.Revision, detail.InventoryDigest, detail.Item)
				continue
			}
			files := make(map[string]string, len(detail.FileURLs))
			for _, file := range detail.FileURLs {
				files[file.Path] = file.URL
			}
			entryPath := strings.TrimSuffix(detail.Item.Root, "/") + "/" + detail.Item.EntryPoint
			entryFound := false
			for _, screen := range detail.Item.Screens {
				inventory = append(inventory, item.ID+":"+screen.Path)
				path := strings.TrimSuffix(detail.Item.Root, "/") + "/" + screen.Path
				fileURL := files[path]
				if fileURL == "" {
					t.Errorf("Prototype %q screen %q is missing from canonical file URLs", item.ID, path)
					continue
				}
				entryFound = entryFound || path == entryPath
				readVisualHTML(t, client, base+fileURL)
			}
			if !entryFound {
				t.Errorf("Prototype %q entry point %q is absent from selected screens", item.ID, entryPath)
			}
		}
		sort.Strings(inventory)
		encoded, err := json.Marshal(inventory)
		if err != nil {
			t.Fatalf("encode selected Screen inventory: %v", err)
		}
		actualInventoryDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(encoded))
		if actualInventoryDigest != expectedInventoryDigest {
			t.Errorf("selected Screen inventory digest = %q, want pinned digest %q", actualInventoryDigest, expectedInventoryDigest)
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
