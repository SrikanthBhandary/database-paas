package service

import (
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"db-paas/pkg/database"
	"db-paas/pkg/repository/memory"
)

var fixedNow = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

// newTestService returns a service backed by the in-memory store and a frozen clock.
func newTestService(t *testing.T) (*Service, *memory.Store) {
	t.Helper()
	store := memory.New()
	svc := New(store)
	svc.now = func() time.Time { return fixedNow }
	return svc, store
}

// validInput is a request that passes every rule. Tests change one field at a time.
func validInput() CreateDatabaseInput {
	return CreateDatabaseInput{
		OwnerID:  "alice",
		Name:     "orders",
		Replicas: 1,
		Engine:   database.EnginePostgres,
		Version:  "17",
		Plan:     "small",
	}
}

func fieldKeys(err error) []string {
	var ve *database.ValidationError
	if !errors.As(err, &ve) {
		return nil
	}
	return slices.Sorted(maps.Keys(ve.Fields))
}

// ---- CreateDatabase: success ----

func TestCreateDatabase_Success(t *testing.T) {
	svc, store := newTestService(t)
	plan := plans["small"]

	in := validInput()
	in.Replicas = 1

	got, err := svc.CreateDatabase(t.Context(), in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if _, err := uuid.Parse(got.ID); err != nil {
		t.Errorf("id %q is not a uuid: %v", got.ID, err)
	}
	if got.Status != database.StatusPending {
		t.Errorf("status = %q, want pending", got.Status)
	}
	if got.StatusReason != "" {
		t.Errorf("status_reason = %q, want empty", got.StatusReason)
	}
	if got.OwnerID != "alice" || got.Name != "orders" ||
		got.Engine != database.EnginePostgres || got.Version != "17" {
		t.Errorf("identity fields wrong: %+v", got)
	}
	if got.Plan != "small" {
		t.Errorf("plan = %q, want small", got.Plan)
	}
	if got.Resources != plan.Resources {
		t.Errorf("resources = %+v, want plan resources %+v", got.Resources, plan.Resources)
	}
	if got.Replicas != 1 {
		t.Errorf("replicas = %d, want 1", got.Replicas)
	}
	if got.Backup != defaultBackup {
		t.Errorf("backup = %+v, want default %+v", got.Backup, defaultBackup)
	}
	if !got.CreatedAt.Equal(fixedNow) || !got.UpdatedAt.Equal(fixedNow) {
		t.Errorf("timestamps = %v / %v, want %v", got.CreatedAt, got.UpdatedAt, fixedNow)
	}

	// it was actually persisted
	stored, err := store.Get(t.Context(), got.ID)
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if stored != got {
		t.Errorf("stored %+v != returned %+v", stored, got)
	}
}

func TestCreateDatabase_StorageOverride(t *testing.T) {
	svc, _ := newTestService(t)
	plan := plans["small"]

	in := validInput()
	in.StorageGB = plan.MaxStorageGB // the largest allowed override

	got, err := svc.CreateDatabase(t.Context(), in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got.Resources.StorageGB != plan.MaxStorageGB {
		t.Errorf("storage = %d, want %d", got.Resources.StorageGB, plan.MaxStorageGB)
	}
	// CPU and memory still come from the plan
	if got.Resources.CPUMillicores != plan.Resources.CPUMillicores ||
		got.Resources.MemoryMB != plan.Resources.MemoryMB {
		t.Errorf("cpu/memory changed by storage override: %+v", got.Resources)
	}
}

func TestCreateDatabase_CustomBackup(t *testing.T) {
	svc, _ := newTestService(t)

	in := validInput()
	in.Backup = &database.Backup{Enabled: true, Schedule: "0 3 * * 0", RetentionDays: 3}

	got, err := svc.CreateDatabase(t.Context(), in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got.Backup != *in.Backup {
		t.Errorf("backup = %+v, want %+v", got.Backup, *in.Backup)
	}
}

func TestCreateDatabase_BackupDisabled(t *testing.T) {
	svc, _ := newTestService(t)

	in := validInput()
	in.Backup = &database.Backup{} // explicitly off

	got, err := svc.CreateDatabase(t.Context(), in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got.Backup.Enabled {
		t.Errorf("backup should be disabled, got %+v", got.Backup)
	}
}

func TestCreateDatabase_AutoscaleEnabled(t *testing.T) {
	svc, _ := newTestService(t)
	plan := plans["small"]

	in := validInput()
	in.AutoScale = database.Autoscale{Enabled: true, MaxStorageGB: plan.MaxStorageGB}

	got, err := svc.CreateDatabase(t.Context(), in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got.Autoscale != in.AutoScale {
		t.Errorf("autoscale = %+v, want %+v", got.Autoscale, in.AutoScale)
	}
}

// ---- CreateDatabase: validation ----

func TestCreateDatabase_Validation(t *testing.T) {
	small := plans["small"]

	tests := []struct {
		name   string
		mutate func(*CreateDatabaseInput)
		want   []string // exact set of invalid fields, sorted; nil = valid
	}{
		{"valid defaults", func(in *CreateDatabaseInput) {}, nil},

		// owner
		{"empty owner", func(in *CreateDatabaseInput) { in.OwnerID = "" }, []string{"owner_id"}},

		// name
		{"empty name", func(in *CreateDatabaseInput) { in.Name = "" }, []string{"name"}},
		{"uppercase name", func(in *CreateDatabaseInput) { in.Name = "Orders" }, []string{"name"}},
		{"name starts with digit", func(in *CreateDatabaseInput) { in.Name = "1orders" }, []string{"name"}},
		{"name ends with hyphen", func(in *CreateDatabaseInput) { in.Name = "orders-" }, []string{"name"}},
		{"name with underscore", func(in *CreateDatabaseInput) { in.Name = "my_db" }, []string{"name"}},
		{"name 41 chars", func(in *CreateDatabaseInput) { in.Name = strings.Repeat("a", 41) }, []string{"name"}},
		{"name 40 chars ok", func(in *CreateDatabaseInput) { in.Name = strings.Repeat("a", 40) }, nil},
		{"single letter name ok", func(in *CreateDatabaseInput) { in.Name = "a" }, nil},
		{"hyphenated name ok", func(in *CreateDatabaseInput) { in.Name = "my-db-1" }, nil},

		// engine and version
		{"unknown engine", func(in *CreateDatabaseInput) { in.Engine = "oracle" }, []string{"engine"}},
		// Test for future
		// {"unsupported version", func(in *CreateDatabaseInput) { in.Version = "9" }, []string{"version"}},
		// {"empty version", func(in *CreateDatabaseInput) { in.Version = "" }, []string{"version"}},
		// {"postgres version on mysql", func(in *CreateDatabaseInput) {
		// 	in.Engine = database.EngineMySQL // version "17" does not exist for mysql
		// }, []string{"version"}},
		// {"mysql 8.4 ok", func(in *CreateDatabaseInput) {
		// 	in.Engine = database.EngineMySQL
		// 	in.Version = "8.4"
		// }, nil},

		// plan
		{"unknown plan", func(in *CreateDatabaseInput) { in.Plan = "huge" }, []string{"plan"}},
		{"empty plan", func(in *CreateDatabaseInput) { in.Plan = "" }, []string{"plan"}},
		{"plan-dependent rules skipped when plan is invalid", func(in *CreateDatabaseInput) {
			in.Plan = "huge"
			in.StorageGB = 99999
			in.Replicas = 99
		}, []string{"plan"}},

		// storage
		{"negative storage", func(in *CreateDatabaseInput) { in.StorageGB = -1 }, []string{"storage_gb"}},
		{"storage below plan default", func(in *CreateDatabaseInput) {
			in.StorageGB = small.Resources.StorageGB - 1
		}, []string{"storage_gb"}},
		{"storage above plan max", func(in *CreateDatabaseInput) {
			in.StorageGB = small.MaxStorageGB + 1
		}, []string{"storage_gb"}},
		{"storage at plan default ok", func(in *CreateDatabaseInput) {
			in.StorageGB = small.Resources.StorageGB
		}, nil},

		// replicas
		{"negative replicas", func(in *CreateDatabaseInput) { in.Replicas = -1 }, []string{"replicas"}},

		{"zero replicas ok", func(in *CreateDatabaseInput) { in.Replicas = 0 }, []string{"replicas"}},

		// autoscale
		{"autoscale max below allocated", func(in *CreateDatabaseInput) {
			in.AutoScale = database.Autoscale{Enabled: true, MaxStorageGB: small.Resources.StorageGB - 1}
		}, []string{"autoscale.max_storage_gb"}},
		{"autoscale max equal to allocated", func(in *CreateDatabaseInput) {
			in.AutoScale = database.Autoscale{Enabled: true, MaxStorageGB: small.Resources.StorageGB}
		}, []string{"autoscale.max_storage_gb"}},
		{"autoscale max above plan limit", func(in *CreateDatabaseInput) {
			in.AutoScale = database.Autoscale{Enabled: true, MaxStorageGB: small.MaxStorageGB + 1}
		}, []string{"autoscale.max_storage_gb"}},
		{"autoscale enabled without max", func(in *CreateDatabaseInput) {
			in.AutoScale = database.Autoscale{Enabled: true}
		}, []string{"autoscale.max_storage_gb"}},
		{"autoscale max uses the storage override", func(in *CreateDatabaseInput) {
			in.StorageGB = 20
			in.AutoScale = database.Autoscale{Enabled: true, MaxStorageGB: 15} // above plan default, below override
		}, []string{"autoscale.max_storage_gb"}},
		{"autoscale disabled but max set", func(in *CreateDatabaseInput) {
			in.AutoScale = database.Autoscale{Enabled: false, MaxStorageGB: 50}
		}, []string{"autoscale.max_storage_gb"}},
		{"autoscale valid", func(in *CreateDatabaseInput) {
			in.AutoScale = database.Autoscale{Enabled: true, MaxStorageGB: small.MaxStorageGB}
		}, nil},

		// backup
		{"backup every minute", func(in *CreateDatabaseInput) {
			in.Backup = &database.Backup{Enabled: true, Schedule: "* * * * *", RetentionDays: 1}
		}, []string{"backup.schedule"}},
		{"backup macro schedule", func(in *CreateDatabaseInput) {
			in.Backup = &database.Backup{Enabled: true, Schedule: "@daily", RetentionDays: 1}
		}, []string{"backup.schedule"}},
		{"backup garbage schedule", func(in *CreateDatabaseInput) {
			in.Backup = &database.Backup{Enabled: true, Schedule: "a b c d e", RetentionDays: 1}
		}, []string{"backup.schedule"}},
		{"backup enabled with empty schedule", func(in *CreateDatabaseInput) {
			in.Backup = &database.Backup{Enabled: true, Schedule: "", RetentionDays: 1}
		}, []string{"backup.schedule"}},
		{"backup retention zero", func(in *CreateDatabaseInput) {
			in.Backup = &database.Backup{Enabled: true, Schedule: "0 2 * * *", RetentionDays: 0}
		}, []string{"backup.retention_days"}},
		{"backup retention above plan max", func(in *CreateDatabaseInput) {
			in.Backup = &database.Backup{Enabled: true, Schedule: "0 2 * * *", RetentionDays: small.MaxRetentionDays + 1}
		}, []string{"backup.retention_days"}},
		{"backup retention at plan max ok", func(in *CreateDatabaseInput) {
			in.Backup = &database.Backup{Enabled: true, Schedule: "0 2 * * *", RetentionDays: small.MaxRetentionDays}
		}, nil},
		{"backup disabled but schedule set", func(in *CreateDatabaseInput) {
			in.Backup = &database.Backup{Enabled: false, Schedule: "0 2 * * *"}
		}, []string{"backup.schedule"}},
		{"backup disabled but retention set", func(in *CreateDatabaseInput) {
			in.Backup = &database.Backup{Enabled: false, RetentionDays: 7}
		}, []string{"backup.retention_days"}},
		{"backup disabled cleanly", func(in *CreateDatabaseInput) {
			in.Backup = &database.Backup{}
		}, nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := newTestService(t)
			in := validInput()
			tc.mutate(&in)

			_, err := svc.CreateDatabase(t.Context(), in)

			if tc.want == nil {
				if err != nil {
					t.Fatalf("unexpected error: %v (fields %v)", err, fieldKeys(err))
				}
				return
			}
			if err == nil {
				t.Fatalf("expected invalid fields %v, got no error", tc.want)
			}
			if !errors.Is(err, database.ErrInvalidInput) {
				t.Errorf("err = %v, want errors.Is(ErrInvalidInput)", err)
			}
			if got := fieldKeys(err); !slices.Equal(got, tc.want) {
				t.Errorf("invalid fields = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCreateDatabase_ReportsAllErrorsAtOnce(t *testing.T) {
	svc, _ := newTestService(t)

	in := CreateDatabaseInput{
		OwnerID: "",         // owner_id
		Name:    "Bad_Name", // name
		Engine:  "oracle",   // engine
		Version: "17",
		Plan:    "huge", // plan
	}

	_, err := svc.CreateDatabase(t.Context(), in)

	want := []string{"engine", "name", "owner_id", "plan", "replicas"}
	if got := fieldKeys(err); !slices.Equal(got, want) {
		t.Errorf("invalid fields = %v, want %v", got, want)
	}
}

func TestCreateDatabase_InvalidInputIsNotStored(t *testing.T) {
	svc, store := newTestService(t)

	in := validInput()
	in.Name = "Bad_Name"
	if _, err := svc.CreateDatabase(t.Context(), in); err == nil {
		t.Fatal("expected a validation error")
	}

	got, _ := store.List(t.Context(), "alice")
	if len(got) != 0 {
		t.Errorf("store has %d rows after a rejected request, want 0", len(got))
	}
}

// ---- CreateDatabase: store errors pass through ----

func TestCreateDatabase_DuplicateNameSameOwner(t *testing.T) {
	svc, _ := newTestService(t)

	if _, err := svc.CreateDatabase(t.Context(), validInput()); err != nil {
		t.Fatalf("first create: %v", err.Error())
	}
	_, err := svc.CreateDatabase(t.Context(), validInput())
	if !errors.Is(err, database.ErrAlreadyExists) {
		t.Fatalf("err = %v, want ErrAlreadyExists", err)
	}
}

func TestCreateDatabase_SameNameDifferentOwner(t *testing.T) {
	svc, _ := newTestService(t)

	if _, err := svc.CreateDatabase(t.Context(), validInput()); err != nil {
		t.Fatalf("alice create: %v", err)
	}
	in := validInput()
	in.OwnerID = "bob"
	if _, err := svc.CreateDatabase(t.Context(), in); err != nil {
		t.Fatalf("bob create: %v", err)
	}
}

func TestCreateDatabase_GeneratesUniqueIDs(t *testing.T) {
	svc, _ := newTestService(t)

	a := validInput()
	a.Name = "first"
	b := validInput()
	b.Name = "second"

	da, _ := svc.CreateDatabase(t.Context(), a)
	db, _ := svc.CreateDatabase(t.Context(), b)
	if da.ID == "" || da.ID == db.ID {
		t.Errorf("ids not unique: %q vs %q", da.ID, db.ID)
	}
}

// ---- GetDatabase ----

func TestGetDatabase(t *testing.T) {
	svc, _ := newTestService(t)
	created, err := svc.CreateDatabase(t.Context(), validInput())
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := svc.GetDatabase(t.Context(), "alice", created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != created {
		t.Errorf("got %+v, want %+v", got, created)
	}
}

func TestGetDatabase_OtherOwnerSeesNotFound(t *testing.T) {
	svc, _ := newTestService(t)
	created, _ := svc.CreateDatabase(t.Context(), validInput())

	_, err := svc.GetDatabase(t.Context(), "bob", created.ID)
	if !errors.Is(err, database.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound (must not reveal that the row exists)", err)
	}
}

func TestGetDatabase_UnknownID(t *testing.T) {
	svc, _ := newTestService(t)

	_, err := svc.GetDatabase(t.Context(), "alice", uuid.NewString())
	if !errors.Is(err, database.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// ---- ListDatabases ----

func TestListDatabases(t *testing.T) {
	svc, _ := newTestService(t)

	for _, name := range []string{"a", "b"} {
		in := validInput()
		in.Name = name
		if _, err := svc.CreateDatabase(t.Context(), in); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}
	other := validInput()
	other.OwnerID = "bob"
	if _, err := svc.CreateDatabase(t.Context(), other); err != nil {
		t.Fatalf("bob create: %v", err)
	}

	got, err := svc.ListDatabases(t.Context(), "alice")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (only alice's)", len(got))
	}
	for _, d := range got {
		if d.OwnerID != "alice" {
			t.Errorf("leaked another owner's database: %+v", d)
		}
	}
}

func TestListDatabases_Empty(t *testing.T) {
	svc, _ := newTestService(t)

	got, err := svc.ListDatabases(t.Context(), "nobody")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

// ---- validateSchedule ----

func TestValidateSchedule(t *testing.T) {
	tests := []struct {
		name    string
		expr    string
		wantErr bool
	}{
		{"daily at 2am", "0 2 * * *", false},
		{"hourly (exactly the minimum)", "0 * * * *", false},
		{"weekly on sunday", "0 3 * * 0", false},
		{"every minute", "* * * * *", true},
		{"every 30 minutes", "*/30 * * * *", true},
		{"two runs 30 minutes apart", "0,30 2 * * *", true},
		{"macro @daily", "@daily", true},
		{"macro @every", "@every 1h", true},
		{"six fields (seconds)", "0 0 2 * * *", true},
		{"garbage", "a b c d e", true},
		{"too few fields", "0 2 * *", true},
		{"empty", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSchedule(tc.expr)
			if (err != nil) != tc.wantErr {
				t.Errorf("validateSchedule(%q) err = %v, wantErr %v", tc.expr, err, tc.wantErr)
			}
		})
	}
}

func intp(v int) *int       { return &v }
func strp(s string) *string { return &s }

// readyDatabase creates a database and walks it to ready the way the worker would.
func readyDatabase(t *testing.T, svc *Service, store *memory.Store, in CreateDatabaseInput) database.Database {
	t.Helper()
	created, err := svc.CreateDatabase(t.Context(), in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got, _ := store.ClaimPending(t.Context(), 1, time.Hour); len(got) != 1 {
		t.Fatalf("claim = %+v", got)
	}
	if err := store.MarkReady(t.Context(), created.ID, database.Connection{Host: "h"}); err != nil {
		t.Fatalf("mark ready: %v", err)
	}
	got, err := svc.GetDatabase(t.Context(), in.OwnerID, created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	return got
}

func smallInput() CreateDatabaseInput {
	in := validInput()
	in.Replicas = 1
	return in
}

func TestUpdateDatabase_Validation(t *testing.T) {
	small := plans["small"]
	tests := []struct {
		name string
		in   UpdateDatabaseInput
		want []string // invalid fields; nil = accepted
	}{
		{"cpu at plan max", UpdateDatabaseInput{CPUMillicores: intp(small.MaxCPUMillicores)}, nil},
		{"cpu above plan max", UpdateDatabaseInput{CPUMillicores: intp(small.MaxCPUMillicores + 1)}, []string{"cpu_millicores"}},
		{"cpu below minimum", UpdateDatabaseInput{CPUMillicores: intp(minCPUMillicores - 1)}, []string{"cpu_millicores"}},
		{"memory at plan max", UpdateDatabaseInput{MemoryMB: intp(small.MaxMemoryMB)}, nil},
		{"memory above plan max", UpdateDatabaseInput{MemoryMB: intp(small.MaxMemoryMB + 1)}, []string{"memory_mb"}},
		{"memory below minimum", UpdateDatabaseInput{MemoryMB: intp(minMemoryMB - 1)}, []string{"memory_mb"}},
		{"storage grows", UpdateDatabaseInput{StorageGB: intp(small.Resources.StorageGB + 1)}, nil},
		{"storage shrinks", UpdateDatabaseInput{StorageGB: intp(small.Resources.StorageGB - 1)}, []string{"storage_gb"}},
		{"storage above plan max", UpdateDatabaseInput{StorageGB: intp(small.MaxStorageGB + 1)}, []string{"storage_gb"}},
		{"replicas zero", UpdateDatabaseInput{Replicas: intp(0)}, []string{"replicas"}},
		{"replicas above plan max", UpdateDatabaseInput{Replicas: intp(small.MaxReplicas + 1)}, []string{"replicas"}},
		{"unknown plan", UpdateDatabaseInput{Plan: strp("huge")}, []string{"plan"}},
		{"several at once", UpdateDatabaseInput{
			CPUMillicores: intp(small.MaxCPUMillicores + 1),
			MemoryMB:      intp(small.MaxMemoryMB + 1),
			Replicas:      intp(0),
		}, []string{"cpu_millicores", "memory_mb", "replicas"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, store := newTestService(t)
			db := readyDatabase(t, svc, store, smallInput())

			_, changed, err := svc.UpdateDatabase(t.Context(), "alice", db.ID, tc.in)
			if tc.want == nil {
				if err != nil || !changed {
					t.Fatalf("changed=%v err=%v, want an accepted change", changed, err)
				}
				return
			}
			if !errors.Is(err, database.ErrInvalidInput) {
				t.Fatalf("err = %v, want ErrInvalidInput", err)
			}
			if got := fieldKeys(err); !slices.Equal(got, tc.want) {
				t.Errorf("invalid fields = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestUpdateDatabase_QueuesChange(t *testing.T) {
	svc, store := newTestService(t)
	db := readyDatabase(t, svc, store, smallInput())

	got, changed, err := svc.UpdateDatabase(t.Context(), "alice", db.ID,
		UpdateDatabaseInput{CPUMillicores: intp(1000), MemoryMB: intp(2048), StorageGB: intp(20)})
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if got.Status != database.StatusUpdatePending {
		t.Errorf("status = %q, want update_pending", got.Status)
	}
	want := database.Resources{CPUMillicores: 1000, MemoryMB: 2048, StorageGB: 20}
	if got.Resources != want {
		t.Errorf("resources = %+v, want %+v", got.Resources, want)
	}
	stored, _ := store.Get(t.Context(), db.ID)
	if stored.Status != database.StatusUpdatePending || stored.Resources != want {
		t.Errorf("not persisted: %+v", stored)
	}
}

func TestUpdateDatabase_NoOp(t *testing.T) {
	svc, store := newTestService(t)
	db := readyDatabase(t, svc, store, smallInput())

	got, changed, err := svc.UpdateDatabase(t.Context(), "alice", db.ID,
		UpdateDatabaseInput{CPUMillicores: intp(db.Resources.CPUMillicores)})
	if err != nil || changed {
		t.Fatalf("changed=%v err=%v, want a no-op", changed, err)
	}
	if got.Status != database.StatusReady {
		t.Errorf("status = %q, a no-op must not queue work", got.Status)
	}
}

func TestUpdateDatabase_PlanUpgradeResetsCPUAndMemoryOnly(t *testing.T) {
	svc, store := newTestService(t)
	db := readyDatabase(t, svc, store, smallInput())
	medium := plans["medium"]

	got, _, err := svc.UpdateDatabase(t.Context(), "alice", db.ID, UpdateDatabaseInput{Plan: strp("medium")})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got.Plan != "medium" ||
		got.Resources.CPUMillicores != medium.Resources.CPUMillicores ||
		got.Resources.MemoryMB != medium.Resources.MemoryMB {
		t.Errorf("cpu/memory not reset to the medium defaults: %+v", got)
	}
	if got.Resources.StorageGB != db.Resources.StorageGB || got.Replicas != db.Replicas {
		t.Errorf("storage/replicas must be kept: %+v", got)
	}
}

func TestUpdateDatabase_DowngradeBlockedByStorage(t *testing.T) {
	svc, store := newTestService(t)
	in := smallInput()
	in.Plan = "medium"
	in.StorageGB = plans["small"].MaxStorageGB + 1 // fits medium, not small
	db := readyDatabase(t, svc, store, in)

	_, _, err := svc.UpdateDatabase(t.Context(), "alice", db.ID, UpdateDatabaseInput{Plan: strp("small")})
	if got := fieldKeys(err); !slices.Equal(got, []string{"plan"}) {
		t.Fatalf("invalid fields = %v, want [plan] (storage cannot shrink to fit)", got)
	}
}

func TestUpdateDatabase_StorageMustStayBelowAutoscaleCeiling(t *testing.T) {
	svc, store := newTestService(t)
	in := smallInput()
	in.AutoScale = database.Autoscale{Enabled: true, MaxStorageGB: 50}
	db := readyDatabase(t, svc, store, in)

	_, _, err := svc.UpdateDatabase(t.Context(), "alice", db.ID, UpdateDatabaseInput{StorageGB: intp(50)})
	if got := fieldKeys(err); !slices.Equal(got, []string{"storage_gb"}) {
		t.Fatalf("invalid fields = %v, want [storage_gb]", got)
	}
}

// func TestUpdateDatabase_OnlyWhenReadyOrUpdateFailed(t *testing.T) {
// 	svc, store := newTestService(t)
// 	pending, _ := svc.CreateDatabase(t.Context(), smallInput()) // still pending
//
// 	_, _, err := svc.UpdateDatabase(t.Context(), "alice", pending.ID, UpdateDatabaseInput{CPUMillicores: intp(1000)})
// 	if !errors.Is(err, database.ErrInvalidState) {
// 		t.Fatalf("pending: err = %v, want ErrInvalidState", err)
// 	}
//
// 	// and while a previous update is still queued
// 	other := smallInput()
// 	other.Name = "second"
// 	db := readyDatabase(t, svc, store, other)
// 	if _, _, err := svc.UpdateDatabase(t.Context(), "alice", db.ID, UpdateDatabaseInput{CPUMillicores: intp(1000)}); err != nil {
// 		t.Fatalf("first update: %v", err)
// 	}
// 	_, _, err = svc.UpdateDatabase(t.Context(), "alice", db.ID, UpdateDatabaseInput{MemoryMB: intp(2048)})
// 	if !errors.Is(err, database.ErrInvalidState) {
// 		t.Fatalf("second update while update_pending: err = %v, want ErrInvalidState", err)
// 	}
// }

func TestUpdateDatabase_OtherOwnerSeesNotFound(t *testing.T) {
	svc, store := newTestService(t)
	db := readyDatabase(t, svc, store, smallInput())

	_, _, err := svc.UpdateDatabase(t.Context(), "bob", db.ID, UpdateDatabaseInput{CPUMillicores: intp(1000)})
	if !errors.Is(err, database.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}
