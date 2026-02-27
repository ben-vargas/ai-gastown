package daemon

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

const (
	defaultWispReaperInterval = 30 * time.Minute
	wispReaperQueryTimeout    = 30 * time.Second
	// Wisps older than this are reaped (closed).
	defaultWispMaxAge = 24 * time.Hour
	// Alert threshold: if open wisp count exceeds this, escalate.
	wispAlertThreshold = 500
)

// WispReaperConfig holds configuration for the wisp_reaper patrol.
type WispReaperConfig struct {
	// Enabled controls whether the reaper runs.
	Enabled bool `json:"enabled"`

	// IntervalStr is how often to run, as a string (e.g., "30m").
	IntervalStr string `json:"interval,omitempty"`

	// MaxAgeStr is how old a wisp must be before reaping (e.g., "24h").
	MaxAgeStr string `json:"max_age,omitempty"`

	// Databases lists specific database names to reap.
	// If empty, auto-discovers from dolt server.
	Databases []string `json:"databases,omitempty"`
}

// wispReaperInterval returns the configured interval, or the default (30m).
func wispReaperInterval(config *DaemonPatrolConfig) time.Duration {
	if config != nil && config.Patrols != nil && config.Patrols.WispReaper != nil {
		if config.Patrols.WispReaper.IntervalStr != "" {
			if d, err := time.ParseDuration(config.Patrols.WispReaper.IntervalStr); err == nil && d > 0 {
				return d
			}
		}
	}
	return defaultWispReaperInterval
}

// wispReaperMaxAge returns the configured max age, or the default (24h).
func wispReaperMaxAge(config *DaemonPatrolConfig) time.Duration {
	if config != nil && config.Patrols != nil && config.Patrols.WispReaper != nil {
		if config.Patrols.WispReaper.MaxAgeStr != "" {
			if d, err := time.ParseDuration(config.Patrols.WispReaper.MaxAgeStr); err == nil && d > 0 {
				return d
			}
		}
	}
	return defaultWispMaxAge
}

// reapWisps closes stale wisps across all configured databases.
// Non-fatal: errors are logged but don't stop the daemon.
func (d *Daemon) reapWisps() {
	if !IsPatrolEnabled(d.patrolConfig, "wisp_reaper") {
		return
	}

	config := d.patrolConfig.Patrols.WispReaper
	maxAge := wispReaperMaxAge(d.patrolConfig)
	cutoff := time.Now().UTC().Add(-maxAge)

	// Determine databases to reap.
	databases := config.Databases
	if len(databases) == 0 {
		databases = d.discoverDoltDatabases()
	}
	if len(databases) == 0 {
		d.logger.Printf("wisp_reaper: no databases to reap")
		return
	}

	totalReaped := 0
	totalOpen := 0

	for _, dbName := range databases {
		if !validDBName.MatchString(dbName) {
			d.logger.Printf("wisp_reaper: skipping invalid database name: %q", dbName)
			continue
		}

		reaped, open, err := d.reapWispsInDB(dbName, cutoff)
		if err != nil {
			d.logger.Printf("wisp_reaper: %s: error: %v", dbName, err)
			continue
		}
		totalReaped += reaped
		totalOpen += open

		if reaped > 0 {
			d.logger.Printf("wisp_reaper: %s: reaped %d stale wisps (older than %v), %d open remain",
				dbName, reaped, maxAge, open)
		}
	}

	if totalReaped > 0 {
		d.logger.Printf("wisp_reaper: total reaped %d stale wisps across %d databases, %d open remain",
			totalReaped, len(databases), totalOpen)
	}

	// Alert if open wisp count is too high.
	if totalOpen > wispAlertThreshold {
		d.logger.Printf("wisp_reaper: WARNING: %d open wisps exceed threshold %d — investigate wisp lifecycle",
			totalOpen, wispAlertThreshold)
	}
}

// reapWispsInDB closes stale wisps in a single database.
// Returns (reaped count, remaining open count, error).
func (d *Daemon) reapWispsInDB(dbName string, cutoff time.Time) (int, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), wispReaperQueryTimeout)
	defer cancel()

	dsn := fmt.Sprintf("root@tcp(%s:%d)/%s?parseTime=true&timeout=5s&readTimeout=10s&writeTimeout=10s",
		"127.0.0.1", d.doltServerPort(), dbName)

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return 0, 0, fmt.Errorf("open connection: %w", err)
	}
	defer db.Close()

	// Close stale open wisps (status=open, created before cutoff).
	// Also close stale hooked/in_progress wisps — these are abandoned molecule steps.
	closeQuery := fmt.Sprintf(
		"UPDATE `%s`.wisps SET status='closed', closed_at=NOW() WHERE status IN ('open', 'hooked', 'in_progress') AND created_at < ?",
		dbName)
	result, err := db.ExecContext(ctx, closeQuery, cutoff)
	if err != nil {
		return 0, 0, fmt.Errorf("close stale wisps: %w", err)
	}

	reaped, _ := result.RowsAffected()

	// Count remaining open wisps.
	var openCount int
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM `%s`.wisps WHERE status IN ('open', 'hooked', 'in_progress')", dbName)
	if err := db.QueryRowContext(ctx, countQuery).Scan(&openCount); err != nil {
		return int(reaped), 0, fmt.Errorf("count open wisps: %w", err)
	}

	return int(reaped), openCount, nil
}

// discoverDoltDatabases returns the list of known production databases.
// Hardcoded for now — matches the databases in daemon.json and dolt-data.
func (d *Daemon) discoverDoltDatabases() []string {
	return []string{"hq", "beads", "gastown"}
}

// doltServerPort returns the configured Dolt server port.
func (d *Daemon) doltServerPort() int {
	if d.doltServer != nil {
		return d.doltServer.config.Port
	}
	return 3307 // Default
}
