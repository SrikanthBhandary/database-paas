package cnpg

import (
	"context"
	"errors"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"db-paas/pkg/database"
)

// BackupStore is the S3-compatible bucket backups are written to.
// Dev only: every tenant shares one credential pair.
type BackupStore struct {
	Bucket          string
	EndpointURL     string // as seen from inside the cluster
	AccessKeyID     string
	SecretAccessKey string
}

type Config struct {
	FieldManager      string        // server-side-apply owner (default "db-paas")
	ImageRepo         string        // default ghcr.io/cloudnative-pg/postgresql
	StorageClass      string        // empty = cluster default
	PollInterval      time.Duration // readiness poll (default 3s)
	Backup            *BackupStore  // nil = backups unavailable
	UpdateStablePolls int           // consecutive healthy polls required after an update (default 5)
}

type Provisioner struct {
	client dynamic.Interface
	cfg    Config
}

func New(client dynamic.Interface, cfg Config) *Provisioner {
	if cfg.FieldManager == "" {
		cfg.FieldManager = "db-paas"
	}
	if cfg.ImageRepo == "" {
		cfg.ImageRepo = "ghcr.io/cloudnative-pg/postgresql"
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 3 * time.Second
	}
	// in New(), alongside the other defaults
	if cfg.UpdateStablePolls <= 0 {
		cfg.UpdateStablePolls = 5
	}

	return &Provisioner{client: client, cfg: cfg}
}

// RESTConfig loads in-cluster credentials when running in a pod, otherwise
// the kubeconfig (explicit path, $KUBECONFIG, or ~/.kube/config).
func RESTConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig == "" {
		if cfg, err := rest.InClusterConfig(); err == nil {
			return cfg, nil
		}
	}
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		rules.ExplicitPath = kubeconfig
	}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{}).ClientConfig()
}

// Provision creates everything for db and returns once the Cluster is
// healthy. Every step is a server-side apply, so running it again after a
// crash converges on the same objects instead of duplicating them.
func (p *Provisioner) Provision(ctx context.Context, db database.Database) (database.Connection, error) {
	if db.Engine != database.EnginePostgres {
		return database.Connection{}, fmt.Errorf("engine %q is not supported by the cnpg provisioner", db.Engine)
	}
	if db.Backup.Enabled && p.cfg.Backup == nil {
		return database.Connection{}, errors.New("backups are enabled but no backup store is configured")
	}

	ns := namespaceFor(db.OwnerID)
	if err := p.checkOwnership(ctx, ns, db); err != nil {
		return database.Connection{}, err
	}

	if err := p.apply(ctx, gvrNamespace, "", namespaceManifest(ns, db.OwnerID)); err != nil {
		return database.Connection{}, err
	}
	if db.Backup.Enabled {
		b := *p.cfg.Backup
		if err := p.apply(ctx, gvrSecret, ns, backupSecretManifest(ns, b)); err != nil {
			return database.Connection{}, err
		}
		// the ObjectStore must exist before the Cluster that references it
		if err := p.apply(ctx, gvrObjectStore, ns, objectStoreManifest(ns, db, b)); err != nil {
			return database.Connection{}, err
		}
	}
	if err := p.apply(ctx, gvrCluster, ns, clusterManifest(ns, db, p.cfg)); err != nil {
		return database.Connection{}, err
	}
	if db.Backup.Enabled {
		if err := p.apply(ctx, gvrScheduledBackup, ns, scheduledBackupManifest(ns, db)); err != nil {
			return database.Connection{}, err
		}
	}

	// in Provision, replace the final wait
	polls := 1
	if db.Status == database.StatusUpdating {
		polls = p.cfg.UpdateStablePolls
	}
	if err := p.waitReady(ctx, ns, db.Name, polls); err != nil {
		return database.Connection{}, err
	}
	return connectionFor(ns, db), nil
}

// checkOwnership refuses to adopt a Cluster that carries another database's ID.
func (p *Provisioner) checkOwnership(ctx context.Context, ns string, db database.Database) error {
	existing, err := p.client.Resource(gvrCluster).Namespace(ns).Get(ctx, db.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get cluster %s/%s: %w", ns, db.Name, err)
	}
	if id := existing.GetLabels()[labelDatabaseID]; id != db.ID {
		return fmt.Errorf("cluster %s/%s belongs to database %q, not %q", ns, db.Name, id, db.ID)
	}
	return nil
}

func (p *Provisioner) apply(ctx context.Context, gvr schema.GroupVersionResource, ns string, obj *unstructured.Unstructured) error {
	var ri dynamic.ResourceInterface = p.client.Resource(gvr)
	if ns != "" {
		ri = p.client.Resource(gvr).Namespace(ns)
	}
	_, err := ri.Apply(ctx, obj.GetName(), obj, metav1.ApplyOptions{
		FieldManager: p.cfg.FieldManager,
		Force:        true, // we are the owner of these fields; take them back if edited by hand
	})
	if err != nil {
		return fmt.Errorf("apply %s %s/%s: %w", gvr.Resource, ns, obj.GetName(), err)
	}
	return nil
}

func (p *Provisioner) waitReady(ctx context.Context, ns, name string, stablePolls int) error {
	ticker := time.NewTicker(p.cfg.PollInterval)
	defer ticker.Stop()

	var last string
	streak := 0
	for {
		c, err := p.client.Resource(gvrCluster).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
		switch {
		case err != nil:
			streak = 0
			last = "get cluster: " + err.Error() // transient: keep polling
		default:
			ready, summary := clusterStatus(c)
			last = summary
			if ready {
				if streak++; streak >= stablePolls {
					return nil
				}
			} else {
				streak = 0
			}
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("cluster %s/%s not ready (%s): %w", ns, name, last, ctx.Err())
		case <-ticker.C:
		}
	}
}
