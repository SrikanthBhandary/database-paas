// Package fake is a stand-in provisioner for local development.
package fake

import (
	"context"
	"errors"
	"strings"
	"time"

	"db-paas/pkg/database"
)

type Provisioner struct {
	Delay time.Duration
}

// Provision waits Delay, then succeeds. Names starting with "fail-" fail,
// so you can exercise the failure path with curl.
func (p Provisioner) Provision(ctx context.Context, db database.Database) (database.Connection, error) {
	t := time.NewTimer(p.Delay)
	defer t.Stop()

	select {
	case <-t.C:
	case <-ctx.Done():
		return database.Connection{}, ctx.Err()
	}
	if strings.HasPrefix(db.Name, "fail-") {
		return database.Connection{}, errors.New("simulated provisioning failure")
	}
	return database.Connection{}, nil
}

// Deprovision waits Delay, then succeeds. Names starting with "faildel-"
// fail, so you can exercise the delete_failed path (they provision fine).
func (p Provisioner) Deprovision(ctx context.Context, db database.Database) error {
	t := time.NewTimer(p.Delay)
	defer t.Stop()

	select {
	case <-t.C:
	case <-ctx.Done():
		return ctx.Err()
	}
	if strings.HasPrefix(db.Name, "faildel-") {
		return errors.New("simulated teardown failure")
	}
	return nil
}
