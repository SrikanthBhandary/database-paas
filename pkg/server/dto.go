package server

import (
	"time"

	"db-paas/pkg/database"
	"db-paas/pkg/service"
)

type createDatabaseRequest struct {
	Name      string        `json:"name"`
	Engine    string        `json:"engine"`
	Version   string        `json:"version"`
	Plan      string        `json:"plan"`
	StorageGB int           `json:"storage_gb"` // optional, 0 = plan default
	Replicas  int           `json:"replicas"`
	Autoscale *autoscaleDTO `json:"autoscale"` // optional
	Backup    *backupDTO    `json:"backup"`    // optional, omitted = platform default
}

type autoscaleDTO struct {
	Enabled      bool `json:"enabled"`
	MaxStorageGB int  `json:"max_storage_gb"`
}

type backupDTO struct {
	Enabled       bool   `json:"enabled"`
	Schedule      string `json:"schedule"` // 5-field cron, UTC
	RetentionDays int    `json:"retention_days"`
}

type resourcesDTO struct {
	CPUMillicores int `json:"cpu_millicores"`
	MemoryMB      int `json:"memory_mb"`
	StorageGB     int `json:"storage_gb"`
}

type databaseResponse struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	Engine       string       `json:"engine"`
	Version      string       `json:"version"`
	Plan         string       `json:"plan"`
	Status       string       `json:"status"`
	StatusReason string       `json:"status_reason,omitempty"`
	Resources    resourcesDTO `json:"resources"`
	Replicas     int          `json:"replicas"`
	Autoscale    autoscaleDTO `json:"autoscale"`
	Backup       backupDTO    `json:"backup"`
	CreatedAt    time.Time    `json:"created_at"`
	UpdatedAt    time.Time    `json:"updated_at"`
}

// A wrapper object (not a bare array) so pagination can be added later
// without breaking clients.
type listDatabasesResponse struct {
	Databases []databaseResponse `json:"databases"`
}

func (req createDatabaseRequest) toInput(ownerID string) service.CreateDatabaseInput {
	in := service.CreateDatabaseInput{
		OwnerID:   ownerID,
		Name:      req.Name,
		Engine:    database.Engine(req.Engine),
		Version:   req.Version,
		Plan:      req.Plan,
		StorageGB: req.StorageGB,
		Replicas:  req.Replicas,
	}
	if req.Autoscale != nil {
		in.AutoScale = database.Autoscale{
			Enabled:      req.Autoscale.Enabled,
			MaxStorageGB: req.Autoscale.MaxStorageGB,
		}
	}
	if req.Backup != nil {
		in.Backup = &database.Backup{
			Enabled:       req.Backup.Enabled,
			Schedule:      req.Backup.Schedule,
			RetentionDays: req.Backup.RetentionDays,
		}
	}
	return in
}

func toResponse(db database.Database) databaseResponse {
	return databaseResponse{
		ID:           db.ID,
		Name:         db.Name,
		Engine:       string(db.Engine),
		Version:      db.Version,
		Plan:         db.Plan,
		Status:       string(db.Status),
		StatusReason: db.StatusReason,
		Resources: resourcesDTO{
			CPUMillicores: db.Resources.CPUMillicores,
			MemoryMB:      db.Resources.MemoryMB,
			StorageGB:     db.Resources.StorageGB,
		},
		Replicas:  db.Replicas,
		Autoscale: autoscaleDTO{Enabled: db.Autoscale.Enabled, MaxStorageGB: db.Autoscale.MaxStorageGB},
		Backup: backupDTO{
			Enabled:       db.Backup.Enabled,
			Schedule:      db.Backup.Schedule,
			RetentionDays: db.Backup.RetentionDays,
		},
		CreatedAt: db.CreatedAt,
		UpdatedAt: db.UpdatedAt,
	}
}

func toListResponse(dbs []database.Database) listDatabasesResponse {
	out := make([]databaseResponse, 0, len(dbs)) // non-nil: encodes as [], not null
	for _, db := range dbs {
		out = append(out, toResponse(db))
	}
	return listDatabasesResponse{Databases: out}
}
