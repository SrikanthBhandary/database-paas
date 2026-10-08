package database

import "time"

type Status string
type Engine string

const (
	EnginePostgres Engine = "postgres"
	EngineMySQL    Engine = "mysql"
)

const (
	StatusPending       Status = "pending"
	StatusProvisioning  Status = "provisioning"
	StatusReady         Status = "ready"
	StatusFailed        Status = "failed"
	StatusDeleting      Status = "deleting"
	StatusUpdatePending Status = "update_pending" // change accepted, waiting for a worker
	StatusUpdating      Status = "updating"       // a worker is applying it
	StatusUpdateFailed  Status = "update_failed"  // did not apply; PATCH again to retry
)

func (e Engine) Valid() bool {
	return e == EnginePostgres || e == EngineMySQL
}

func (s Status) Valid() bool {
	switch s {
	case StatusPending, StatusProvisioning, StatusReady, StatusFailed, StatusDeleting,
		StatusUpdatePending, StatusUpdating, StatusUpdateFailed:
		return true
	}
	return false
}

// Spec is the part of a database a client can change after creation.
type Spec struct {
	Plan      string
	Resources Resources
	Replicas  int
}


type Database struct {
	ID           string
	Name         string
	Replicas     int
	Engine       Engine
	Version      string
	Plan         string
	Status       Status
	StatusReason string
	OwnerID      string
	Resources    Resources
	Autoscale    Autoscale // This is to autoscale the database size
	Backup       Backup
	Connection   Connection
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type Resources struct {
	CPUMillicores int // 1000 = 1 vCPU
	MemoryMB      int
	StorageGB     int
}

type Autoscale struct {
	Enabled      bool
	MaxStorageGB int // ceiling
}

type Backup struct {
	Enabled       bool
	Schedule      string // cron expression, e.g. "0 2 * * *"
	RetentionDays int
}

// Connection says where a ready database can be reached. It is empty until
// provisioning finishes. It never contains a password: the secret is only
// referenced, so credentials stay in Kubernetes.
type Connection struct {
	Host            string
	Port            int
	Database        string
	Username        string
	SecretNamespace string // where the credentials live
	SecretName      string
}

func (d Database) Spec() Spec {
	return Spec{Plan: d.Plan, Resources: d.Resources, Replicas: d.Replicas}
}
