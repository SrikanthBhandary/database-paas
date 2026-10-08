package cnpg

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"db-paas/pkg/database"
)

func testDB() database.Database {
	return database.Database{
		ID: "11111111-2222-3333-4444-555555555555", Name: "orders",
		Engine: database.EnginePostgres, Version: "17", Plan: "small", OwnerID: "alice",
		Resources: database.Resources{CPUMillicores: 500, MemoryMB: 1024, StorageGB: 10},
		Replicas:  2,
		Backup:    database.Backup{Enabled: true, Schedule: "0 2 * * *", RetentionDays: 7},
	}
}

var testCfg = Config{ImageRepo: "ghcr.io/cloudnative-pg/postgresql"}
var testStore = BackupStore{Bucket: "cnpg-backups", EndpointURL: "http://minio.minio.svc:9000", AccessKeyID: "k", SecretAccessKey: "s"}

func TestNamespaceFor(t *testing.T) {
	a, b := namespaceFor("alice"), namespaceFor("bob")
	if a == b {
		t.Fatal("different owners must get different namespaces")
	}
	if a != namespaceFor("alice") {
		t.Error("namespace must be deterministic")
	}
	if !strings.HasPrefix(a, "tenant-") || len(a) != len("tenant-")+16 {
		t.Errorf("unexpected namespace %q", a)
	}
	// hostile owner IDs still produce a valid name
	if got := namespaceFor("Weird Owner/../ID!"); strings.ContainsAny(got, " /!.ABCDEFGHIJKLMNOPQRSTUVWXYZ") {
		t.Errorf("namespace %q is not DNS-safe", got)
	}
}

// A plain int in an unstructured object makes DeepCopy panic at runtime.
func TestManifestsAreDeepCopySafe(t *testing.T) {
	db := testDB()
	objs := map[string]*unstructured.Unstructured{
		"namespace":       namespaceManifest("tenant-x", "alice"),
		"secret":          backupSecretManifest("ns", testStore),
		"objectstore":     objectStoreManifest("ns", db, testStore),
		"cluster":         clusterManifest("ns", db, testCfg),
		"scheduledbackup": scheduledBackupManifest("ns", db),
	}
	for name, o := range objs {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("DeepCopy panicked (a non-JSON type such as int in the manifest): %v", r)
				}
			}()
			o.DeepCopy()
		})
	}
}

func TestClusterManifest(t *testing.T) {
	db := testDB()
	c := clusterManifest("ns", db, Config{ImageRepo: "ghcr.io/cloudnative-pg/postgresql", StorageClass: "fast"})

	if n, _, _ := unstructured.NestedInt64(c.Object, "spec", "instances"); n != 2 {
		t.Errorf("instances = %d, want 2 (replicas is the total)", n)
	}
	if v, _, _ := unstructured.NestedString(c.Object, "spec", "imageName"); v != "ghcr.io/cloudnative-pg/postgresql:17" {
		t.Errorf("imageName = %q", v)
	}
	if v, _, _ := unstructured.NestedString(c.Object, "spec", "storage", "size"); v != "10Gi" {
		t.Errorf("storage size = %q", v)
	}
	if v, _, _ := unstructured.NestedString(c.Object, "spec", "storage", "storageClass"); v != "fast" {
		t.Errorf("storageClass = %q", v)
	}
	for _, k := range []string{"requests", "limits"} {
		if v, _, _ := unstructured.NestedString(c.Object, "spec", "resources", k, "cpu"); v != "500m" {
			t.Errorf("%s cpu = %q", k, v)
		}
		if v, _, _ := unstructured.NestedString(c.Object, "spec", "resources", k, "memory"); v != "1024Mi" {
			t.Errorf("%s memory = %q", k, v)
		}
	}
	if c.GetName() != "orders" || c.GetNamespace() != "ns" {
		t.Errorf("name/namespace = %s/%s", c.GetNamespace(), c.GetName())
	}
	if c.GetLabels()[labelDatabaseID] != db.ID {
		t.Errorf("missing database-id label: %v", c.GetLabels())
	}
}

func TestClusterManifestPlugins(t *testing.T) {
	db := testDB()

	plugins, found, _ := unstructured.NestedSlice(clusterManifest("ns", db, testCfg).Object, "spec", "plugins")
	if !found || len(plugins) != 1 {
		t.Fatalf("plugins = %v, want one entry when backups are enabled", plugins)
	}
	p := plugins[0].(map[string]any)
	if p["name"] != pluginName || p["isWALArchiver"] != true {
		t.Errorf("plugin = %v", p)
	}
	if got := p["parameters"].(map[string]any)["barmanObjectName"]; got != "orders-store" {
		t.Errorf("barmanObjectName = %v", got)
	}

	db.Backup = database.Backup{}
	if _, found, _ := unstructured.NestedSlice(clusterManifest("ns", db, testCfg).Object, "spec", "plugins"); found {
		t.Error("plugins must be absent when backups are disabled")
	}
}

func TestObjectStoreManifest(t *testing.T) {
	db := testDB()
	o := objectStoreManifest("ns", db, testStore)

	if v, _, _ := unstructured.NestedString(o.Object, "spec", "retentionPolicy"); v != "7d" {
		t.Errorf("retentionPolicy = %q", v)
	}
	want := "s3://cnpg-backups/" + db.ID + "/"
	if v, _, _ := unstructured.NestedString(o.Object, "spec", "configuration", "destinationPath"); v != want {
		t.Errorf("destinationPath = %q, want %q", v, want)
	}
}

func TestScheduledBackupAddsSecondsField(t *testing.T) {
	s := scheduledBackupManifest("ns", testDB())
	if v, _, _ := unstructured.NestedString(s.Object, "spec", "schedule"); v != "0 0 2 * * *" {
		t.Errorf("schedule = %q, want a 6-field cron", v)
	}
	if v, _, _ := unstructured.NestedString(s.Object, "spec", "cluster", "name"); v != "orders" {
		t.Errorf("cluster ref = %q", v)
	}
}

func TestClusterStatus(t *testing.T) {
	build := func(phase string, ready, want int64, readyCond bool) *unstructured.Unstructured {
		status := map[string]any{"phase": phase, "readyInstances": ready}
		if readyCond {
			status["conditions"] = []any{map[string]any{"type": "Ready", "status": "True"}}
		}
		return &unstructured.Unstructured{Object: map[string]any{
			"spec": map[string]any{"instances": want}, "status": status,
		}}
	}
	tests := []struct {
		name string
		obj  *unstructured.Unstructured
		want bool
	}{
		{"healthy phase", build(phaseHealthy, 2, 2, false), true},
		{"ready condition without phase text", build("", 2, 2, true), true},
		{"healthy but replicas missing", build(phaseHealthy, 1, 2, false), false},
		{"still creating", build("Setting up primary", 0, 2, false), false},
		{"no status yet", &unstructured.Unstructured{Object: map[string]any{
			"spec": map[string]any{"instances": int64(2)}}}, false},
		{"zero instances", build(phaseHealthy, 0, 0, false), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got, summary := clusterStatus(tc.obj); got != tc.want {
				t.Errorf("ready = %v (%s), want %v", got, summary, tc.want)
			}
		})
	}
}

func TestProvisionRejectsEarly(t *testing.T) {
	p := New(nil, Config{}) // nil client: these paths must return before using it

	mysql := testDB()
	mysql.Engine = database.EngineMySQL
	if err := p.Provision(t.Context(), mysql); err == nil {
		t.Error("mysql should be rejected")
	}

	if err := p.Provision(t.Context(), testDB()); err == nil {
		t.Error("backups enabled without a configured store should be rejected")
	}
}
