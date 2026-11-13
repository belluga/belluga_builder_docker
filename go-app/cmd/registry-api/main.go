package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"belluga-builder-go/internal/registry"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "import":
			if err := runImport(os.Args[2:]); err != nil {
				log.Fatal(err)
			}
			return
		case "provision-selected-todos":
			if err := runProvisionSelectedTODOs(os.Args[2:]); err != nil {
				log.Fatal(err)
			}
			return
		case "provision-registered-todos":
			if err := runProvisionRegisteredTODOs(os.Args[2:]); err != nil {
				log.Fatal(err)
			}
			return
		case "provision-todo-history":
			if err := runProvisionTODOHistory(os.Args[2:]); err != nil {
				log.Fatal(err)
			}
			return
		case "check-todo-history":
			if err := runCheckTODOHistory(os.Args[2:]); err != nil {
				log.Fatal(err)
			}
			return
		case "diagnose-todo-schema":
			if err := runDiagnoseTODOCollectionSchema(os.Args[2:]); err != nil {
				log.Fatal(err)
			}
			return
		}
	}
	uri := strings.TrimSpace(os.Getenv("REGISTRY_MONGODB_URI"))
	controlDatabase := strings.TrimSpace(os.Getenv("REGISTRY_CONTROL_DATABASE"))
	if uri == "" {
		log.Fatal("REGISTRY_MONGODB_URI is required")
	}
	store, err := registry.OpenMongo(context.Background(), uri, controlDatabase)
	if err != nil {
		log.Fatal("registry storage is unavailable")
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = store.Close(ctx)
	}()
	if err := store.ConfigureProofReader(strings.TrimSpace(os.Getenv("DELPHI_PROJECT_READER_URL")), strings.TrimSpace(os.Getenv("DELPHI_PROJECT_READER_TOKEN"))); err != nil {
		log.Fatal("private proof Reader is unavailable")
	}
	if stateRoot := strings.TrimSpace(os.Getenv("DELPHI_PROJECT_READER_STATE_ROOT")); stateRoot != "" {
		if err := store.ConfigureDesignSystemSourceMapRoot(stateRoot); err != nil {
			log.Fatal("private Design System source-map state is unavailable")
		}
	}

	address := strings.TrimSpace(os.Getenv("LISTEN_ADDR"))
	if address == "" {
		address = "0.0.0.0:8080"
	}
	server := &http.Server{
		Addr: address, Handler: registry.NewHandler(store),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second,
		MaxHeaderBytes: 16 << 10,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serveError := make(chan error, 1)
	go func() { serveError <- server.ListenAndServe() }()
	select {
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			log.Printf("registry API shutdown failed")
		}
	case err := <-serveError:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Fatal("registry API failed")
		}
	}
}

func runProvisionRegisteredTODOs(args []string) error {
	if len(args) != 0 {
		return errors.New("provision-registered-todos accepts no tenant arguments; Companies are discovered from the control registry")
	}
	uri := strings.TrimSpace(os.Getenv("REGISTRY_MONGODB_URI"))
	if uri == "" {
		return errors.New("REGISTRY_MONGODB_URI is required to provision registered Companies")
	}
	store, err := registry.OpenMongo(context.Background(), uri, strings.TrimSpace(os.Getenv("REGISTRY_CONTROL_DATABASE")))
	if err != nil {
		return errors.New("MongoDB connection unavailable; no Company schema was provisioned")
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = store.Close(ctx)
	}()
	report, err := store.ProvisionRegisteredTODOs(context.Background())
	if err != nil {
		return err
	}
	data, err := json.Marshal(report)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(os.Stdout, string(data)); err != nil {
		return err
	}
	if !report.Verified {
		return errors.New("TEACH: at least one registered Company failed TODO schema provisioning; inspect its company_id and phase in the report")
	}
	return nil
}

func runProvisionSelectedTODOs(args []string) error {
	if len(args) != 0 {
		return errors.New("provision-selected-todos accepts no arguments; it is bounded to belluga-solutions")
	}
	uri := strings.TrimSpace(os.Getenv("REGISTRY_MONGODB_URI"))
	if uri == "" {
		return errors.New("REGISTRY_MONGODB_URI is required to provision selected TODOs")
	}
	store, err := registry.OpenMongo(context.Background(), uri, strings.TrimSpace(os.Getenv("REGISTRY_CONTROL_DATABASE")))
	if err != nil {
		return errors.New("MongoDB connection unavailable; TODO collection was not provisioned")
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = store.Close(ctx)
	}()
	if err := store.ProvisionTODOCollection(context.Background(), "belluga-solutions"); err != nil {
		return errors.New("selected TODO collection provisioning/readback failed; import was not attempted")
	}
	return printTODOProvisionReport()
}

func runProvisionTODOHistory(args []string) error {
	if len(args) != 1 || !registry.ValidCompanyID(args[0]) {
		return errors.New("provision-todo-history requires one valid Company ID; database selection is server-resolved")
	}
	uri := strings.TrimSpace(os.Getenv("REGISTRY_MONGODB_URI"))
	if uri == "" {
		return errors.New("REGISTRY_MONGODB_URI is required to provision TODO history")
	}
	store, err := registry.OpenMongo(context.Background(), uri, strings.TrimSpace(os.Getenv("REGISTRY_CONTROL_DATABASE")))
	if err != nil {
		return errors.New("MongoDB connection unavailable; TODO history was not provisioned")
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = store.Close(ctx)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := store.ProvisionTODOHistory(ctx, args[0]); err != nil {
		return fmt.Errorf("TODO history provisioning/baseline failed: %w", err)
	}
	report, err := store.PreflightTODOHistory(ctx, args[0])
	if err != nil {
		return fmt.Errorf("TODO history post-provision verification failed: %w", err)
	}
	if !report.CoverageComplete {
		return errors.New("TODO history post-provision verification is incomplete; current TODOs and truthful baselines do not reconcile")
	}
	data, err := json.Marshal(report)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(os.Stdout, string(data))
	return err
}

func runCheckTODOHistory(args []string) error {
	if len(args) != 1 || !registry.ValidCompanyID(args[0]) {
		return errors.New("check-todo-history requires one valid Company ID; database selection is server-resolved")
	}
	uri := strings.TrimSpace(os.Getenv("REGISTRY_MONGODB_URI"))
	if uri == "" {
		return errors.New("REGISTRY_MONGODB_URI is required to inspect TODO history")
	}
	store, err := registry.OpenMongo(context.Background(), uri, strings.TrimSpace(os.Getenv("REGISTRY_CONTROL_DATABASE")))
	if err != nil {
		return errors.New("MongoDB connection unavailable; no history state was changed")
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = store.Close(ctx)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	report, err := store.PreflightTODOHistory(ctx, args[0])
	if err != nil {
		return fmt.Errorf("TODO history read-only preflight failed: %w", err)
	}
	data, err := json.Marshal(report)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(os.Stdout, string(data))
	return err
}

func runDiagnoseTODOCollectionSchema(args []string) error {
	if len(args) != 1 || !registry.ValidCompanyID(args[0]) {
		return errors.New("diagnose-todo-schema requires one valid Company ID; database selection is server-resolved")
	}
	uri := strings.TrimSpace(os.Getenv("REGISTRY_MONGODB_URI"))
	if uri == "" {
		return errors.New("REGISTRY_MONGODB_URI is required for TODO schema diagnostics")
	}
	store, err := registry.OpenMongo(context.Background(), uri, strings.TrimSpace(os.Getenv("REGISTRY_CONTROL_DATABASE")))
	if err != nil {
		return errors.New("MongoDB connection unavailable; no data was changed")
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = store.Close(ctx)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	report, err := store.DiagnoseTODOCollectionSchema(ctx, args[0])
	if err != nil {
		return fmt.Errorf("read-only TODO schema diagnostic failed: %w", err)
	}
	data, err := json.Marshal(report)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(os.Stdout, string(data))
	return err
}

func printTODOProvisionReport() error {
	data, err := json.Marshal(struct {
		State      string `json:"state"`
		CompanyID  string `json:"company_id"`
		Collection string `json:"collection"`
	}{"provisioned_and_verified", "belluga-solutions", "todos"})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(os.Stdout, string(data))
	return err
}

type sourceRegistry struct {
	SchemaVersion      string                         `json:"schema_version"`
	Companies          map[string]registry.Company    `json:"companies"`
	Projects           map[string]registry.Project    `json:"projects"`
	Preferences        map[string]registry.Preference `json:"preferences"`
	LifecycleMigration *registry.LifecycleMigration   `json:"lifecycle_migration"`
}

func runImport(args []string) error {
	flags := flag.NewFlagSet("registry-api import", flag.ContinueOnError)
	stateRoot := flags.String("state-root", "", "configured Builder private state root containing registry.json")
	apply := flags.Bool("apply", false, "apply the validated import to the configured MongoDB")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if !filepath.IsAbs(*stateRoot) {
		return errors.New("an absolute configured state-root is required")
	}
	registryBytes, err := readBounded(filepath.Join(filepath.Clean(*stateRoot), "registry.json"), 1<<20)
	if err != nil {
		return errors.New("source registry is unavailable or exceeds the import limit")
	}
	var source sourceRegistry
	if json.Unmarshal(registryBytes, &source) != nil {
		return errors.New("source registry JSON is invalid")
	}
	boora, ok := source.Projects["boora"]
	if !ok || boora.Binding == nil {
		return errors.New("configured registry has no bound Bóora Project")
	}
	roadmapPath, err := boundRoadmapPath(boora.Binding)
	if err != nil {
		return errors.New("configured Bóora Foundation binding is invalid")
	}
	roadmapBytes, err := readBounded(roadmapPath, 2<<20)
	if err != nil {
		return errors.New("Bóora Roadmap is unavailable or exceeds the import limit")
	}
	roadmap, err := registry.ValidateLegacyRoadmap(roadmapBytes)
	if err != nil {
		return errors.New("Bóora Roadmap JSON is invalid")
	}
	input := registry.LegacyImport{SchemaVersion: source.SchemaVersion, Companies: source.Companies, Projects: source.Projects, Preferences: source.Preferences, LifecycleMigration: source.LifecycleMigration, Roadmap: roadmap}
	report, _, _, err := registry.ValidateImport(input)
	if err != nil {
		return errors.New("source parity does not match the approved two-Company, three-Project, four-Epic, one-Release import")
	}
	if !*apply {
		return printImportReport("dry_run_no_writes", report)
	}
	uri := strings.TrimSpace(os.Getenv("REGISTRY_MONGODB_URI"))
	if uri == "" {
		return errors.New("REGISTRY_MONGODB_URI is required to apply import")
	}
	store, err := registry.OpenMongo(context.Background(), uri, strings.TrimSpace(os.Getenv("REGISTRY_CONTROL_DATABASE")))
	if err != nil {
		return errors.New("MongoDB is unavailable; import was not applied")
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = store.Close(ctx)
	}()
	report, err = store.Import(context.Background(), input)
	if err != nil {
		return errors.New("Mongo import failed without fallback to the source files")
	}
	return printImportReport("applied", report)
}

func boundRoadmapPath(binding *registry.Binding) (string, error) {
	if binding == nil || !filepath.IsAbs(binding.WorkspaceRoot) || filepath.IsAbs(binding.ProjectRoot) || filepath.IsAbs(binding.FoundationRoot) {
		return "", errors.New("invalid Project binding")
	}
	workspace, err := filepath.EvalSymlinks(filepath.Clean(binding.WorkspaceRoot))
	if err != nil {
		return "", err
	}
	projectRoot := filepath.Clean(filepath.Join(workspace, binding.ProjectRoot))
	foundationRoot := filepath.Clean(filepath.Join(projectRoot, binding.FoundationRoot))
	for _, candidate := range []string{projectRoot, foundationRoot} {
		relative, relErr := filepath.Rel(workspace, candidate)
		if relErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return "", errors.New("Project binding escapes workspace")
		}
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(foundationRoot, "roadmap", "catalog.json"))
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(workspace, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("bound Foundation path escapes workspace")
	}
	return resolved, nil
}

func readBounded(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(filepath.Clean(path))
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > limit {
		return nil, errors.New("invalid source file")
	}
	return os.ReadFile(path)
}

func printImportReport(state string, report registry.ImportReport) error {
	data, err := json.Marshal(struct {
		State string `json:"state"`
		registry.ImportReport
	}{state, report})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(os.Stdout, string(data))
	return err
}
