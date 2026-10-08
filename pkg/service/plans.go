package service

import "db-paas/pkg/database"

type Plan struct {
	Name             string
	Resources        database.Resources
	MaxStorageGB     int
	MaxRetentionDays int
}

var plans = map[string]Plan{
	"small":  {"small", database.Resources{CPUMillicores: 500, MemoryMB: 1024, StorageGB: 10}, 100, 7},
	"medium": {"medium", database.Resources{CPUMillicores: 2000, MemoryMB: 4096, StorageGB: 50}, 500, 14},
	"large":  {"large", database.Resources{CPUMillicores: 4000, MemoryMB: 16384, StorageGB: 200}, 2000, 35},
}
