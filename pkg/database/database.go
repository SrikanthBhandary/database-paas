package database

import "time"

type Engine string

func (e Engine) Valid() bool {
	return e == EnginePostgres || e == EngineMySQL
}

func (s Status) Valid() bool {
	switch s {
	case StatusPending, StatusProvisioning, StatusReady, StatusFailed, StatusDeleting:
		return true
	}
	return false
}

const (
	EnginePostgres Engine = "postgres"
	EngineMySQL    Engine = "mysql"
)

type Status string

const (
	StatusPending      Status = "pending"
	StatusProvisioning Status = "provisioning"
	StatusReady        Status = "ready"
	StatusFailed       Status = "failed"
	StatusDeleting     Status = "deleting"
)

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
