package service

import (
	"db-paas/pkg/database"
	"errors"
	"fmt"
	"regexp"

	"github.com/robfig/cron/v3"

	"strings"
	"time"
)

const minBackupInterval = time.Hour

var defaultBackup = database.Backup{
	Enabled:       true,
	Schedule:      "0 2 * * *",
	RetentionDays: 7,
}
var nameRe = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,38}[a-z0-9])?$`)

func validate(in CreateDatabaseInput) (Plan, map[string]string) {
	errs := make(map[string]string)

	plan, planOK := plans[in.Plan]
	if !planOK {
		errs["plan"] = "must be one of: small, medium, large"
	}

	if in.Replicas < 1 {
		errs["replicas"] = "replicas must be one or more"
	}

	if in.OwnerID == "" {
		errs["owner_id"] = "required"
	}
	if !nameRe.MatchString(in.Name) {
		errs["name"] = "must be lowercase letters, digits and hyphens, starting with a letter (max 40)"
	}
	if !in.Engine.Valid() {
		errs["engine"] = "unsupported engine"
	}

	// Support the exact versions for target
	// engine.

	if planOK {
		validateStorage(in, plan, errs)
		validateAutoscale(in, plan, errs)
	}
	validateBackup(resolveBackup(in.Backup), plan, planOK, errs)

	return plan, errs
}

// allocatedResources is what the database will actually get: the plan's
// resources with the optional storage override applied.
func allocatedResources(in CreateDatabaseInput, plan Plan) database.Resources {
	res := plan.Resources
	if in.StorageGB > 0 {
		res.StorageGB = in.StorageGB
	}
	return res
}

func resolveBackup(b *database.Backup) database.Backup {
	if b == nil {
		return defaultBackup
	}
	return *b
}

func validateStorage(in CreateDatabaseInput, plan Plan, errs map[string]string) {
	switch {
	case in.StorageGB < 0:
		errs["storage_gb"] = "must not be negative"
	case in.StorageGB == 0:
		// plan default
	case in.StorageGB < plan.Resources.StorageGB:
		errs["storage_gb"] = fmt.Sprintf("must be at least %d for the %s plan", plan.Resources.StorageGB, plan.Name)
	case in.StorageGB > plan.MaxStorageGB:
		errs["storage_gb"] = fmt.Sprintf("must be at most %d for the %s plan", plan.MaxStorageGB, plan.Name)
	}
}

func validateAutoscale(in CreateDatabaseInput, plan Plan, errs map[string]string) {
	a := in.AutoScale

	if !a.Enabled {
		// no contradictory config: a ceiling without autoscaling makes no sense
		if a.MaxStorageGB != 0 {
			errs["autoscale.max_storage_gb"] = "must be empty when autoscale is disabled"
		}
		return
	}

	alloc := allocatedResources(in, plan).StorageGB
	switch {
	case a.MaxStorageGB <= alloc:
		errs["autoscale.max_storage_gb"] =
			fmt.Sprintf("must be greater than the allocated storage (%d GB)", alloc)
	case a.MaxStorageGB > plan.MaxStorageGB:
		errs["autoscale.max_storage_gb"] =
			fmt.Sprintf("must be at most %d for the %s plan", plan.MaxStorageGB, plan.Name)
	}
}

func validateBackup(b database.Backup, plan Plan, planOK bool, errs map[string]string) {
	if !b.Enabled {
		if b.Schedule != "" {
			errs["backup.schedule"] = "must be empty when backups are disabled"
		}
		if b.RetentionDays != 0 {
			errs["backup.retention_days"] = "must be 0 when backups are disabled"
		}
		return
	}

	if b.RetentionDays < 1 {
		errs["backup.retention_days"] = "must be at least 1"
	} else if planOK && b.RetentionDays > plan.MaxRetentionDays {
		errs["backup.retention_days"] =
			fmt.Sprintf("must be at most %d for the %s plan", plan.MaxRetentionDays, plan.Name)
	}

	if err := validateSchedule(b.Schedule); err != nil {
		errs["backup.schedule"] = err.Error()
	}
}

func validateSchedule(expr string) error {
	// Exactly 5 fields. This also rejects macros like "@every 1m" and "@daily",
	// which the cron parser would otherwise accept.
	if len(strings.Fields(expr)) != 5 {
		return errors.New("must be a standard 5-field cron expression (UTC), e.g. \"0 2 * * *\"")
	}
	sched, err := cron.ParseStandard(expr)
	if err != nil {
		return errors.New("invalid cron expression")
	}

	// Check the gap between consecutive runs over a sample of fire times.
	// Looking at only two runs would miss schedules like "0 2,3 * * *"
	// whose gaps differ.
	prev := sched.Next(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC))
	if prev.IsZero() {
		return errors.New("schedule never runs")
	}
	for range 100 {
		next := sched.Next(prev)
		if next.IsZero() {
			break
		}
		if next.Sub(prev) < minBackupInterval {
			return fmt.Errorf("must not run more often than every %s", minBackupInterval)
		}
		prev = next
	}
	return nil
}
