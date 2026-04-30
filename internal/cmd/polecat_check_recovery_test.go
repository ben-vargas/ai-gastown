package cmd

import (
	"errors"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

// fakeMRFinder is a test stub for the mrFinder interface used by applyMQCheck.
type fakeMRFinder struct {
	issue *beads.Issue
	err   error
}

func (f fakeMRFinder) FindMRForBranchAny(branch string) (*beads.Issue, error) {
	return f.issue, f.err
}

func TestApplyMQCheck(t *testing.T) {
	tests := []struct {
		name            string
		finder          mrFinder
		beadTerminal    bool
		initialVerdict  string
		wantVerdict     string
		wantMQStatus    string
		wantNeedsRecov  bool
	}{
		{
			// The regression this change fixes: assigned bead is CLOSED
			// (e.g. aa-xtee no-op audit). Must NOT return NEEDS_MQ_SUBMIT
			// because there is nothing to submit — the work is terminal.
			name:            "closed bead skips MQ submit check",
			finder:          fakeMRFinder{issue: nil, err: nil},
			beadTerminal:    true,
			initialVerdict:  "SAFE_TO_NUKE",
			wantVerdict:     "SAFE_TO_NUKE",
			wantMQStatus:    "submitted",
			wantNeedsRecov:  false,
		},
		{
			name:            "open bead with no MR escalates to NEEDS_MQ_SUBMIT",
			finder:          fakeMRFinder{issue: nil, err: nil},
			beadTerminal:    false,
			initialVerdict:  "SAFE_TO_NUKE",
			wantVerdict:     "NEEDS_MQ_SUBMIT",
			wantMQStatus:    "not_submitted",
			wantNeedsRecov:  true,
		},
		{
			name:            "open bead with MR stays SAFE_TO_NUKE",
			finder:          fakeMRFinder{issue: &beads.Issue{ID: "mr-1"}, err: nil},
			beadTerminal:    false,
			initialVerdict:  "SAFE_TO_NUKE",
			wantVerdict:     "SAFE_TO_NUKE",
			wantMQStatus:    "submitted",
			wantNeedsRecov:  false,
		},
		{
			name:            "MR lookup error is conservative (unknown, no escalation)",
			finder:          fakeMRFinder{issue: nil, err: errors.New("bd exploded")},
			beadTerminal:    false,
			initialVerdict:  "SAFE_TO_NUKE",
			wantVerdict:     "SAFE_TO_NUKE",
			wantMQStatus:    "unknown",
			wantNeedsRecov:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status := RecoveryStatus{
				Verdict: tt.initialVerdict,
				Branch:  "polecat/test",
			}
			applyMQCheck(&status, tt.finder, tt.beadTerminal)

			if status.Verdict != tt.wantVerdict {
				t.Errorf("Verdict = %q, want %q", status.Verdict, tt.wantVerdict)
			}
			if status.MQStatus != tt.wantMQStatus {
				t.Errorf("MQStatus = %q, want %q", status.MQStatus, tt.wantMQStatus)
			}
			if status.NeedsRecovery != tt.wantNeedsRecov {
				t.Errorf("NeedsRecovery = %v, want %v", status.NeedsRecovery, tt.wantNeedsRecov)
			}
		})
	}
}
