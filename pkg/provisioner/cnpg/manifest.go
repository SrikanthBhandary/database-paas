package cnpg

import (
	"crypto/sha256"
	"db-paas/pkg/database"
	"encoding/hex"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	pgPort            = 5432
	bootstrapDatabase = "app" // CNPG's default initdb database...
	bootstrapUser     = "app" // ...and its owner
	labelManagedBy    = "app.kubernetes.io/managed-by"
	labelDatabaseID   = "paas.db/database-id"
	labelTenant       = "paas.db/tenant"
	annotationOwner   = "paas.db/owner"
	managedByValue    = "db-paas"

	pluginName       = "barman-cloud.cloudnative-pg.io"
	backupSecretName = "backup-s3"
	phaseHealthy     = "Cluster in healthy state"
)

var (
	gvrNamespace       = schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}
	gvrSecret          = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}
	gvrCluster         = schema.GroupVersionResource{Group: "postgresql.cnpg.io", Version: "v1", Resource: "clusters"}
	gvrScheduledBackup = schema.GroupVersionResource{Group: "postgresql.cnpg.io", Version: "v1", Resource: "scheduledbackups"}
	gvrObjectStore     = schema.GroupVersionResource{Group: "barmancloud.cnpg.io", Version: "v1", Resource: "objectstores"}
)

// namespaceFor derives a DNS-safe namespace from an arbitrary owner ID.
func namespaceFor(ownerID string) string {
	sum := sha256.Sum256([]byte(ownerID))
	return "tenant-" + hex.EncodeToString(sum[:8])
}

func objectStoreName(db database.Database) string { return db.Name + "-store" }
func scheduleName(db database.Database) string    { return db.Name + "-schedule" }

// NOTE: unstructured objects must contain only JSON types (string, int64,
// bool, map[string]any, []any). A plain int makes DeepCopy panic, which is
// why every number below is int64.

func newObject(apiVersion, kind, namespace, name string, labels map[string]any) *unstructured.Unstructured {
	meta := map[string]any{"name": name, "labels": labels}
	if namespace != "" {
		meta["namespace"] = namespace
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": apiVersion,
		"kind":       kind,
		"metadata":   meta,
	}}
}

func dbLabels(db database.Database) map[string]any {
	return map[string]any{labelManagedBy: managedByValue, labelDatabaseID: db.ID}
}

func namespaceManifest(ns, ownerID string) *unstructured.Unstructured {
	o := newObject("v1", "Namespace", "", ns, map[string]any{
		labelManagedBy: managedByValue,
		labelTenant:    strings.TrimPrefix(ns, "tenant-"),
	})
	// the owner ID is arbitrary text, so it goes in an annotation, not a label
	o.Object["metadata"].(map[string]any)["annotations"] = map[string]any{annotationOwner: ownerID}
	return o
}

func backupSecretManifest(ns string, b BackupStore) *unstructured.Unstructured {
	o := newObject("v1", "Secret", ns, backupSecretName, map[string]any{labelManagedBy: managedByValue})
	o.Object["type"] = "Opaque"
	o.Object["stringData"] = map[string]any{
		"ACCESS_KEY_ID":     b.AccessKeyID,
		"ACCESS_SECRET_KEY": b.SecretAccessKey,
	}
	return o
}

func objectStoreManifest(ns string, db database.Database, b BackupStore) *unstructured.Unstructured {
	o := newObject("barmancloud.cnpg.io/v1", "ObjectStore", ns, objectStoreName(db), dbLabels(db))
	o.Object["spec"] = map[string]any{
		"retentionPolicy": fmt.Sprintf("%dd", db.Backup.RetentionDays),
		"configuration": map[string]any{
			"destinationPath": fmt.Sprintf("s3://%s/%s/", b.Bucket, db.ID),
			"endpointURL":     b.EndpointURL,
			"s3Credentials": map[string]any{
				"accessKeyId":     map[string]any{"name": backupSecretName, "key": "ACCESS_KEY_ID"},
				"secretAccessKey": map[string]any{"name": backupSecretName, "key": "ACCESS_SECRET_KEY"},
			},
			"wal": map[string]any{"compression": "gzip"},
		},
	}
	return o
}

func resourceList(r database.Resources) map[string]any {
	return map[string]any{
		"cpu":    fmt.Sprintf("%dm", r.CPUMillicores),
		"memory": fmt.Sprintf("%dMi", r.MemoryMB),
	}
}

func clusterManifest(ns string, db database.Database, cfg Config) *unstructured.Unstructured {
	storage := map[string]any{"size": fmt.Sprintf("%dGi", db.Resources.StorageGB)}
	if cfg.StorageClass != "" {
		storage["storageClass"] = cfg.StorageClass
	}

	spec := map[string]any{
		"instances": int64(db.Replicas),
		"imageName": fmt.Sprintf("%s:%s", cfg.ImageRepo, db.Version),
		"storage":   storage,
		"resources": map[string]any{ // requests == limits: guaranteed QoS
			"requests": resourceList(db.Resources),
			"limits":   resourceList(db.Resources),
		},
	}
	if db.Backup.Enabled {
		spec["plugins"] = []any{map[string]any{
			"name":          pluginName,
			"isWALArchiver": true,
			"parameters":    map[string]any{"barmanObjectName": objectStoreName(db)},
		}}
	}

	o := newObject("postgresql.cnpg.io/v1", "Cluster", ns, db.Name, dbLabels(db))
	o.Object["spec"] = spec
	return o
}

func scheduledBackupManifest(ns string, db database.Database) *unstructured.Unstructured {
	o := newObject("postgresql.cnpg.io/v1", "ScheduledBackup", ns, scheduleName(db), dbLabels(db))
	o.Object["spec"] = map[string]any{
		// CNPG cron has a leading seconds field; ours is the standard 5 fields (UTC)
		"schedule":             "0 " + db.Backup.Schedule,
		"backupOwnerReference": "self",
		"cluster":              map[string]any{"name": db.Name},
		"method":               "plugin",
		"pluginConfiguration":  map[string]any{"name": pluginName},
	}
	return o
}

// clusterStatus reports whether the Cluster is healthy, plus a short summary
// for logs and timeout errors.
func clusterStatus(c *unstructured.Unstructured) (ready bool, summary string) {
	phase, _, _ := unstructured.NestedString(c.Object, "status", "phase")
	readyInstances, _, _ := unstructured.NestedInt64(c.Object, "status", "readyInstances")
	want, _, _ := unstructured.NestedInt64(c.Object, "spec", "instances")

	summary = fmt.Sprintf("phase=%q ready=%d/%d", phase, readyInstances, want)
	if want == 0 {
		return false, summary
	}
	healthy := phase == phaseHealthy || hasReadyCondition(c)
	return readyInstances == want && healthy, summary
}

func hasReadyCondition(c *unstructured.Unstructured) bool {
	conds, _, _ := unstructured.NestedSlice(c.Object, "status", "conditions")
	for _, raw := range conds {
		m, ok := raw.(map[string]any)
		if ok && m["type"] == "Ready" && m["status"] == "True" {
			return true
		}
	}
	return false
}

// connectionFor describes how to reach the cluster from inside Kubernetes.
// CNPG publishes the read-write service as <cluster>-rw and the generated
// credentials as the secret <cluster>-app.
func connectionFor(ns string, db database.Database) database.Connection {
	return database.Connection{
		Host:            fmt.Sprintf("%s-rw.%s.svc", db.Name, ns),
		Port:            pgPort,
		Database:        bootstrapDatabase,
		Username:        bootstrapUser,
		SecretNamespace: ns,
		SecretName:      db.Name + "-app",
	}
}
