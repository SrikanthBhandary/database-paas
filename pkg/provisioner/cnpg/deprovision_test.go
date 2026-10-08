package cnpg

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"

	"db-paas/pkg/database"
)

func newFakeClient(objs ...runtime.Object) *dynfake.FakeDynamicClient {
	return dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			gvrCluster:         "ClusterList",
			gvrScheduledBackup: "ScheduledBackupList",
			gvrObjectStore:     "ObjectStoreList",
			gvrPVC:             "PersistentVolumeClaimList",
		}, objs...)
}

func seeded(db database.Database) []runtime.Object {
	ns := namespaceFor(db.OwnerID)
	return []runtime.Object{
		scheduledBackupManifest(ns, db),
		clusterManifest(ns, db, testCfg),
		objectStoreManifest(ns, db, testStore),
	}
}

func exists(t *testing.T, c *dynfake.FakeDynamicClient, gvr schema.GroupVersionResource, ns, name string) bool {
	t.Helper()
	_, err := c.Resource(gvr).Namespace(ns).Get(t.Context(), name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false
	}
	if err != nil {
		t.Fatalf("get %s %s/%s: %v", gvr.Resource, ns, name, err)
	}
	return true
}

func pvcFor(ns, cluster string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "PersistentVolumeClaim",
		"metadata": map[string]any{
			"name": cluster + "-1", "namespace": ns,
			"labels": map[string]any{labelCNPGCluster: cluster},
		},
	}}
}

func TestDeprovision_RemovesEverything(t *testing.T) {
	db := testDB()
	ns := namespaceFor(db.OwnerID)
	c := newFakeClient(seeded(db)...)
	p := New(c, Config{PollInterval: time.Millisecond})

	if err := p.Deprovision(t.Context(), db); err != nil {
		t.Fatalf("deprovision: %v", err)
	}
	for gvr, name := range map[schema.GroupVersionResource]string{
		gvrScheduledBackup: scheduleName(db),
		gvrCluster:         db.Name,
		gvrObjectStore:     objectStoreName(db),
	} {
		if exists(t, c, gvr, ns, name) {
			t.Errorf("%s %s still exists", gvr.Resource, name)
		}
	}
}

func TestDeprovision_IsIdempotent(t *testing.T) {
	db := testDB()
	p := New(newFakeClient(), Config{PollInterval: time.Millisecond}) // nothing exists

	for i := range 2 {
		if err := p.Deprovision(t.Context(), db); err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
	}
}

func TestDeprovision_RefusesAnotherDatabasesCluster(t *testing.T) {
	db := testDB()
	ns := namespaceFor(db.OwnerID)

	other := testDB()
	other.ID = "99999999-9999-9999-9999-999999999999" // same name and owner, different database
	c := newFakeClient(clusterManifest(ns, other, testCfg))
	p := New(c, Config{PollInterval: time.Millisecond})

	err := p.Deprovision(t.Context(), db)
	if err == nil || !strings.Contains(err.Error(), "refusing to delete") {
		t.Fatalf("err = %v, want a refusal", err)
	}
	if !exists(t, c, gvrCluster, ns, db.Name) {
		t.Error("the other database's cluster was deleted")
	}
}

func TestDeprovision_WaitsForVolumeClaimsThenResumes(t *testing.T) {
	db := testDB()
	ns := namespaceFor(db.OwnerID)
	c := newFakeClient(append(seeded(db), pvcFor(ns, db.Name))...)
	p := New(c, Config{PollInterval: time.Millisecond})

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	err := p.Deprovision(ctx, db)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want a deadline error while a volume claim remains", err)
	}
	if exists(t, c, gvrCluster, ns, db.Name) {
		t.Error("cluster should already be deleted; only the volume claim blocks completion")
	}
	if !exists(t, c, gvrObjectStore, ns, objectStoreName(db)) {
		t.Error("object store must outlive the cluster until it is fully gone")
	}

	// the volume goes away; a retry resumes and finishes
	if err := c.Resource(gvrPVC).Namespace(ns).Delete(t.Context(), db.Name+"-1", metav1.DeleteOptions{}); err != nil {
		t.Fatalf("delete pvc: %v", err)
	}
	if err := p.Deprovision(t.Context(), db); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if exists(t, c, gvrObjectStore, ns, objectStoreName(db)) {
		t.Error("object store should be removed on the retry")
	}
}
