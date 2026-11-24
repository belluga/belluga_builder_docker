package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	"belluga-builder-go/internal/registry"
)

func runDiscoveryIndexes(args []string) error {
	if len(args) == 0 || len(args) > 64 {
		return errors.New("discovery-indexes requires 1 to 64 explicit Company IDs")
	}
	for _, id := range args {
		if !registry.ValidCompanyID(id) {
			return errors.New("discovery-indexes requires valid Company IDs")
		}
	}
	uri := strings.TrimSpace(os.Getenv("REGISTRY_MONGODB_URI"))
	if uri == "" {
		return errors.New("REGISTRY_MONGODB_URI is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	store, err := registry.OpenDiscoveryMongo(ctx, uri, strings.TrimSpace(os.Getenv("REGISTRY_CONTROL_DATABASE")))
	if err != nil {
		return errors.New("Registry storage is unavailable for discovery index preparation")
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = store.Close(ctx)
	}()
	ids, err := store.PrepareDiscoveryIndexes(ctx, args)
	if err != nil {
		return errors.New("Discovery indexes were not verified. Confirm the registered Company IDs, storage connectivity and index specifications, then rerun discovery-indexes.")
	}
	return json.NewEncoder(os.Stdout).Encode(struct {
		Verified   bool     `json:"verified"`
		CompanyIDs []string `json:"company_ids"`
	}{true, ids})
}
