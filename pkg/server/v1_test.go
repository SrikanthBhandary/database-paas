package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"db-paas/pkg/database"
	"db-paas/pkg/repository/memory"
	"db-paas/pkg/service"
)

const (
	validBody   = `{"name":"orders","engine":"postgres","version":"17","plan":"small","replicas":1}`
	billingBody = `{"name":"billing","engine":"postgres","version":"17","plan":"small","replicas":1}`
)

// ---- helpers ----

func newTestHandler(t *testing.T) http.Handler {
	t.Helper()
	api := NewAPIServer(service.New(memory.New()), zap.NewNop())
	api.RegisterAPI()
	return api.Router
}

func do(t *testing.T, h http.Handler, method, path, owner, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if owner != "" {
		req.Header.Set("X-Owner-ID", owner)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return v
}

// mustCreate is for test setup: it fails on the spot, with the server's
// message, if the create does not return 202.
func mustCreate(t *testing.T, h http.Handler, owner, body string) databaseResponse {
	t.Helper()
	rec := do(t, h, "POST", "/v1/databases", owner, body)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create: status = %d, body %s", rec.Code, rec.Body)
	}
	return decode[databaseResponse](t, rec)
}

// ---- POST /v1/databases ----

func TestCreate_Accepted(t *testing.T) {
	h := newTestHandler(t)

	rec := do(t, h, "POST", "/v1/databases", "alice", validBody) // the create under test
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q", ct)
	}

	got := decode[databaseResponse](t, rec)
	if _, err := uuid.Parse(got.ID); err != nil {
		t.Errorf("id %q is not a uuid", got.ID)
	}
	if got.Status != "pending" || got.Name != "orders" || got.Plan != "small" {
		t.Errorf("got %+v", got)
	}
	if got.Replicas != 1 {
		t.Errorf("replicas = %d, want 1", got.Replicas)
	}
	if got.Resources.CPUMillicores == 0 {
		t.Errorf("plan resources not resolved: %+v", got.Resources)
	}
	if loc := rec.Header().Get("Location"); loc != "/v1/databases/"+got.ID {
		t.Errorf("location = %q", loc)
	}
}

func TestCreate_MissingOwner(t *testing.T) {
	rec := do(t, newTestHandler(t), "POST", "/v1/databases", "", validBody)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestCreate_ValidationErrors(t *testing.T) {
	body := `{"name":"Bad_Name","engine":"postgres","version":"17","plan":"huge","replicas":1}`
	rec := do(t, newTestHandler(t), "POST", "/v1/databases", "alice", body)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("content-type = %q", ct)
	}
	p := decode[Problem](t, rec)
	if _, ok := p.Errors["name"]; !ok {
		t.Errorf("missing name error: %+v", p.Errors)
	}
	if _, ok := p.Errors["plan"]; !ok {
		t.Errorf("missing plan error: %+v", p.Errors)
	}
	if _, ok := p.Errors["replicas"]; ok {
		t.Errorf("unexpected replicas error: %+v", p.Errors)
	}
}

func TestCreate_BadBodies(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"empty body", ``},
		{"malformed json", `{`},
		{"unknown field status", `{"name":"a","engine":"postgres","version":"17","plan":"small","replicas":1,"status":"ready"}`},
		{"client-supplied owner", `{"name":"a","engine":"postgres","version":"17","plan":"small","replicas":1,"owner_id":"bob"}`},
		{"client-supplied id", `{"id":"x","name":"a","engine":"postgres","version":"17","plan":"small","replicas":1}`},
		{"wrong type", `{"name":"a","engine":"postgres","version":"17","plan":"small","replicas":1,"storage_gb":"ten"}`},
		{"trailing data", validBody + `{}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, newTestHandler(t), "POST", "/v1/databases", "alice", tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400; body %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestCreate_BodyTooLarge(t *testing.T) {
	body := `{"name":"` + strings.Repeat("a", 2*maxBodyBytes) + `"}`
	rec := do(t, newTestHandler(t), "POST", "/v1/databases", "alice", body)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
}

func TestCreate_DuplicateName(t *testing.T) {
	h := newTestHandler(t)
	mustCreate(t, h, "alice", validBody) // first create succeeds

	rec := do(t, h, "POST", "/v1/databases", "alice", validBody) // second conflicts
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "_key") {
		t.Errorf("constraint name leaked: %s", rec.Body)
	}
}

func TestCreate_SameNameDifferentOwner(t *testing.T) {
	h := newTestHandler(t)
	mustCreate(t, h, "alice", validBody)
	mustCreate(t, h, "bob", validBody) // must not conflict
}

// ---- GET /v1/databases/{id} ----

func TestGet(t *testing.T) {
	h := newTestHandler(t)
	created := mustCreate(t, h, "alice", validBody)

	rec := do(t, h, "GET", "/v1/databases/"+created.ID, "alice", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := decode[databaseResponse](t, rec); got.ID != created.ID {
		t.Errorf("id = %q, want %q", got.ID, created.ID)
	}
}

func TestGet_NotFound(t *testing.T) {
	h := newTestHandler(t)
	created := mustCreate(t, h, "alice", validBody)

	tests := []struct {
		name, owner, id string
	}{
		{"other owner", "bob", created.ID},
		{"unknown id", "alice", uuid.NewString()},
		{"malformed id", "alice", "not-a-uuid"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, h, "GET", "/v1/databases/"+tc.id, tc.owner, "")
			if rec.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404", rec.Code)
			}
		})
	}
}

func TestGet_MissingOwner(t *testing.T) {
	h := newTestHandler(t)
	created := mustCreate(t, h, "alice", validBody)

	rec := do(t, h, "GET", "/v1/databases/"+created.ID, "", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

// ---- GET /v1/databases ----

func TestList(t *testing.T) {
	h := newTestHandler(t)
	mustCreate(t, h, "alice", validBody)
	mustCreate(t, h, "alice", billingBody)
	mustCreate(t, h, "bob", validBody)

	rec := do(t, h, "GET", "/v1/databases", "alice", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	got := decode[listDatabasesResponse](t, rec)
	if len(got.Databases) != 2 {
		t.Fatalf("len = %d, want 2 (only alice's)", len(got.Databases))
	}
	for _, d := range got.Databases {
		if d.Name != "orders" && d.Name != "billing" {
			t.Errorf("unexpected database in alice's list: %+v", d)
		}
	}
}

func TestList_EmptyIsArrayNotNull(t *testing.T) {
	rec := do(t, newTestHandler(t), "GET", "/v1/databases", "alice", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"databases":[]`) {
		t.Errorf("body = %s, want an empty array", rec.Body)
	}
}

// ---- infrastructure endpoints ----

func TestInfraEndpointsNeedNoOwner(t *testing.T) {
	h := newTestHandler(t)
	for _, path := range []string{"/health", "/ping"} {
		if rec := do(t, h, "GET", path, "", ""); rec.Code != http.StatusOK {
			t.Errorf("%s status = %d, want 200", path, rec.Code)
		}
	}
}

func TestGet_ConnectionOnlyWhenReady(t *testing.T) {
	store := memory.New()
	api := NewAPIServer(service.New(store), zap.NewNop())
	api.RegisterAPI()
	h := api.Router

	created := mustCreate(t, h, "alice", validBody)
	if created.Connection != nil {
		t.Fatalf("connection = %+v while pending, want none", created.Connection)
	}

	// do what the worker does
	claimed, _ := store.ClaimPending(t.Context(), 1, time.Hour)
	conn := database.Connection{Host: "orders-rw.tenant-x.svc", Port: 5432, Database: "app", Username: "app",
		SecretNamespace: "tenant-x", SecretName: "orders-app"}
	if err := store.MarkReady(t.Context(), claimed[0].ID, conn); err != nil {
		t.Fatalf("mark ready: %v", err)
	}

	rec := do(t, h, "GET", "/v1/databases/"+created.ID, "alice", "")
	got := decode[databaseResponse](t, rec)
	if got.Status != "ready" || got.Connection == nil ||
		got.Connection.Host != conn.Host || got.Connection.Port != 5432 ||
		got.Connection.Database != "app" || got.Connection.Username != "app" {
		t.Errorf("got %+v", got)
	}
	if strings.Contains(rec.Body.String(), "orders-app") || strings.Contains(rec.Body.String(), "tenant-x") && strings.Contains(rec.Body.String(), "secret") {
		t.Errorf("secret reference leaked: %s", rec.Body)
	}
}

func TestPatch(t *testing.T) {
	store := memory.New()
	api := NewAPIServer(service.New(store), zap.NewNop())
	api.RegisterAPI()
	h := api.Router

	created := mustCreate(t, h, "alice", validBody)
	path := "/v1/databases/" + created.ID

	// still pending: not allowed yet
	if rec := do(t, h, "PATCH", path, "alice", `{"cpu_millicores":1000}`); rec.Code != http.StatusConflict {
		t.Fatalf("pending: status = %d, want 409", rec.Code)
	}

	// walk it to ready, as the worker would
	claimed, _ := store.ClaimPending(t.Context(), 1, time.Hour)
	if err := store.MarkReady(t.Context(), claimed[0].ID, database.Connection{Host: "h"}); err != nil {
		t.Fatalf("mark ready: %v", err)
	}

	// invalid values: 400 with field errors (these numbers assume the small plan's limits)
	rec := do(t, h, "PATCH", path, "alice", `{"cpu_millicores":999999,"replicas":0}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid: status = %d, want 400; body %s", rec.Code, rec.Body)
	}
	p := decode[Problem](t, rec)
	if _, ok := p.Errors["cpu_millicores"]; !ok {
		t.Errorf("missing cpu_millicores error: %+v", p.Errors)
	}
	if _, ok := p.Errors["replicas"]; !ok {
		t.Errorf("missing replicas error: %+v", p.Errors)
	}

	// unknown and forbidden fields are rejected
	for _, body := range []string{`{"status":"ready"}`, `{"name":"renamed"}`, `{"engine":"mysql"}`} {
		if rec := do(t, h, "PATCH", path, "alice", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", body, rec.Code)
		}
	}

	// another owner sees 404
	if rec := do(t, h, "PATCH", path, "bob", `{"cpu_millicores":1000}`); rec.Code != http.StatusNotFound {
		t.Errorf("other owner: status = %d, want 404", rec.Code)
	}

	// a patch that changes nothing: 200, no work queued
	rec = do(t, h, "PATCH", path, "alice", `{"cpu_millicores":500}`) // the small plan's default
	if rec.Code != http.StatusOK {
		t.Fatalf("no-op: status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	if got := decode[databaseResponse](t, rec); got.Status != "ready" {
		t.Errorf("no-op status = %q, want ready", got.Status)
	}

	// a real change: 202 + update_pending
	rec = do(t, h, "PATCH", path, "alice", `{"cpu_millicores":1000,"memory_mb":2048}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("update: status = %d, want 202; body %s", rec.Code, rec.Body)
	}
	got := decode[databaseResponse](t, rec)
	if got.Status != "update_pending" || got.Resources.CPUMillicores != 1000 || got.Resources.MemoryMB != 2048 {
		t.Errorf("got %+v", got)
	}
	if loc := rec.Header().Get("Location"); loc != path {
		t.Errorf("location = %q, want %q", loc, path)
	}

	// a second change while the first is queued
	if rec := do(t, h, "PATCH", path, "alice", `{"cpu_millicores":900}`); rec.Code != http.StatusConflict {
		t.Errorf("concurrent update: status = %d, want 409", rec.Code)
	}
}
