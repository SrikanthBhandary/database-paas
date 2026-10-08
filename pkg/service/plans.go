package service

import "db-paas/pkg/database"

type Plan struct {
	Name             string
	Resources        database.Resources
	MaxStorageGB     int
	MaxRetentionDays int
	MaxCPUMillicores int
	MaxMemoryMB      int
	MaxReplicas      int
}

const (
	minCPUMillicores = 250
	minMemoryMB      = 512 // Postgres needs room to run
)

var plans = map[string]Plan{
	"small": {
		Name: "small",
		Resources: database.Resources{CPUMillicores: 500, MemoryMB: 1024, StorageGB: 10},
		MaxCPUMillicores: 1000,
		MaxMemoryMB: 2048,
		MaxStorageGB: 100,
		MaxReplicas: 1,
		MaxRetentionDays: 7,
	},
	"medium": {
		Name: "medium",
		Resources: database.Resources{CPUMillicores: 2000, MemoryMB: 4096, StorageGB: 50},
		MaxCPUMillicores: 4000,
	 	MaxMemoryMB: 8192,
		MaxStorageGB: 500,
	 	MaxReplicas: 3,
		MaxRetentionDays: 14,
	},
	"large": {
		Name: "large",
		Resources: database.Resources{CPUMillicores: 4000, MemoryMB: 16384, StorageGB: 200},
		MaxCPUMillicores: 8000,
	 	MaxMemoryMB: 32768,
		MaxStorageGB: 2000,
		MaxReplicas: 5,
	 	MaxRetentionDays: 35,
	},
}
