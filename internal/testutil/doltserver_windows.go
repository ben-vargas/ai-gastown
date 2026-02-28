//go:build windows

package testutil

import (
	"fmt"
	"testing"
)

// DoltDockerImage is the Docker image used for Dolt test containers.
// Matches MinDoltVersion in internal/deps/dolt.go.
const DoltDockerImage = "dolthub/dolt-sql-server:1.82.4"

// StartIsolatedDoltContainer is not supported on Windows CI.
func StartIsolatedDoltContainer(t *testing.T) string {
	t.Helper()
	t.Skip("Docker not available on Windows CI")
	return ""
}

// EnsureDoltContainerForTestMain is not supported on Windows CI.
func EnsureDoltContainerForTestMain() error {
	return fmt.Errorf("Docker not available on Windows CI")
}

// RequireDoltContainer is not supported on Windows CI.
func RequireDoltContainer(t *testing.T) {
	t.Helper()
	t.Skip("Docker not available on Windows CI")
}

// DoltContainerAddr returns empty string on Windows.
func DoltContainerAddr() string { return "" }

// DoltContainerPort returns empty string on Windows.
func DoltContainerPort() string { return "" }

// TerminateDoltContainer is a no-op on Windows.
func TerminateDoltContainer() {}
