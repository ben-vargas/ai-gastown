package daemon

import (
	"encoding/json"
	"testing"
	"time"
)

func TestDoctorDogInterval(t *testing.T) {
	// Default interval
	if got := doctorDogInterval(nil); got != defaultDoctorDogInterval {
		t.Errorf("expected default interval %v, got %v", defaultDoctorDogInterval, got)
	}

	// Custom interval
	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			DoctorDog: &DoctorDogConfig{
				Enabled:     true,
				IntervalStr: "10m",
			},
		},
	}
	if got := doctorDogInterval(config); got != 10*time.Minute {
		t.Errorf("expected 10m interval, got %v", got)
	}

	// Invalid interval falls back to default
	config.Patrols.DoctorDog.IntervalStr = "invalid"
	if got := doctorDogInterval(config); got != defaultDoctorDogInterval {
		t.Errorf("expected default interval for invalid config, got %v", got)
	}
}

func TestDoctorDogDatabases(t *testing.T) {
	// Default databases
	dbs := doctorDogDatabases(nil)
	if len(dbs) != 6 {
		t.Errorf("expected 6 default databases, got %d", len(dbs))
	}

	// Custom databases
	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			DoctorDog: &DoctorDogConfig{
				Enabled:   true,
				Databases: []string{"hq", "beads"},
			},
		},
	}
	dbs = doctorDogDatabases(config)
	if len(dbs) != 2 {
		t.Errorf("expected 2 custom databases, got %d", len(dbs))
	}
}

func TestIsPatrolEnabled_DoctorDog(t *testing.T) {
	// Nil config: disabled (opt-in patrol)
	if IsPatrolEnabled(nil, "doctor_dog") {
		t.Error("expected doctor_dog to be disabled with nil config")
	}

	// Empty patrols: disabled
	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{},
	}
	if IsPatrolEnabled(config, "doctor_dog") {
		t.Error("expected doctor_dog to be disabled by default")
	}

	// Explicitly enabled
	config.Patrols.DoctorDog = &DoctorDogConfig{Enabled: true}
	if !IsPatrolEnabled(config, "doctor_dog") {
		t.Error("expected doctor_dog to be enabled when configured")
	}

	// Explicitly disabled
	config.Patrols.DoctorDog = &DoctorDogConfig{Enabled: false}
	if IsPatrolEnabled(config, "doctor_dog") {
		t.Error("expected doctor_dog to be disabled when explicitly disabled")
	}
}

func TestDirSize(t *testing.T) {
	dir := t.TempDir()

	// Empty directory
	size, err := dirSize(dir)
	if err != nil {
		t.Fatal(err)
	}
	if size != 0 {
		t.Errorf("expected 0 for empty dir, got %d", size)
	}
}

func TestDoctorDogReportJSON(t *testing.T) {
	report := &DoctorDogReport{
		Timestamp:    time.Date(2026, 2, 27, 12, 0, 0, 0, time.UTC),
		Host:         "127.0.0.1",
		Port:         3307,
		TCPReachable: true,
		Latency:      &DoctorDogLatencyReport{DurationMs: 1.5},
		Databases:    &DoctorDogDatabasesReport{Names: []string{"hq", "beads"}, Count: 2},
		DiskUsage: []DoctorDogDiskReport{
			{Database: "hq", SizeBytes: 1048576, SizeMB: 1},
		},
	}

	data, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("failed to marshal report: %v", err)
	}

	var decoded DoctorDogReport
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal report: %v", err)
	}

	if !decoded.TCPReachable {
		t.Error("expected tcp_reachable=true")
	}
	if decoded.Latency == nil || decoded.Latency.DurationMs != 1.5 {
		t.Error("expected latency 1.5ms")
	}
	if decoded.Databases == nil || decoded.Databases.Count != 2 {
		t.Error("expected 2 databases")
	}
	if len(decoded.DiskUsage) != 1 || decoded.DiskUsage[0].Database != "hq" {
		t.Error("expected disk usage for hq")
	}
}

func TestDoctorDogReportOmitsNilFields(t *testing.T) {
	// Report with only TCP data — should omit nil fields
	report := &DoctorDogReport{
		Timestamp:    time.Date(2026, 2, 27, 12, 0, 0, 0, time.UTC),
		Host:         "127.0.0.1",
		Port:         3307,
		TCPReachable: false,
	}

	data, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("failed to marshal report: %v", err)
	}

	// Verify nil fields are omitted from JSON
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if _, ok := raw["latency"]; ok {
		t.Error("expected latency to be omitted when nil")
	}
	if _, ok := raw["databases"]; ok {
		t.Error("expected databases to be omitted when nil")
	}
	if _, ok := raw["gc"]; ok {
		t.Error("expected gc to be omitted when nil")
	}
}

func TestDoctorDogConfigBackwardsCompat(t *testing.T) {
	// Verify that configs with the old max_db_count field can still be parsed
	// (JSON decoder ignores unknown fields by default).
	jsonData := `{"enabled": true, "interval": "3m", "max_db_count": 10}`

	var config DoctorDogConfig
	if err := json.Unmarshal([]byte(jsonData), &config); err != nil {
		t.Fatalf("failed to unmarshal config with old max_db_count field: %v", err)
	}

	if !config.Enabled {
		t.Error("expected enabled=true")
	}
	if config.IntervalStr != "3m" {
		t.Errorf("expected interval=3m, got %s", config.IntervalStr)
	}
}
