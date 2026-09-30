package session

import "time"

// NewManifest starts a manifest for a command run at now.
func NewManifest(now time.Time, command string) *Manifest {
	return &Manifest{
		Version:   ManifestVersion,
		ID:        NewID(now),
		StartedAt: now.UTC(),
		Command:   command,
		Entries:   []Entry{},
	}
}

// Add appends an entry and keeps ReclaimedBytes in sync.
func (m *Manifest) Add(e Entry) {
	m.Entries = append(m.Entries, e)
	m.RecomputeReclaimed()
}

// Finish marks the session as completed. Sessions that never reach this
// call (crashes) keep a zero FinishedAt and are shown as unfinished.
func (m *Manifest) Finish(now time.Time) {
	m.FinishedAt = now.UTC()
}

// RecomputeReclaimed sets ReclaimedBytes to the sum of SizeBytes over
// applied entries. Failed, skipped and restored entries do not count, so
// undo can call this after marking entries restored.
func (m *Manifest) RecomputeReclaimed() {
	var total int64
	for _, e := range m.Entries {
		if e.Status == StatusApplied {
			total += e.SizeBytes
		}
	}
	m.ReclaimedBytes = total
}

// Counts summarizes the entries of a manifest by status.
type Counts struct {
	Applied    int
	Failed     int
	Skipped    int
	Restored   int
	Restorable int
}

// Counts tallies entries by status. Restorable counts applied entries that
// `brooom undo` can still reverse.
func (m *Manifest) Counts() Counts {
	var c Counts
	for _, e := range m.Entries {
		switch e.Status {
		case StatusApplied:
			c.Applied++
			if e.Restorable {
				c.Restorable++
			}
		case StatusFailed:
			c.Failed++
		case StatusSkipped:
			c.Skipped++
		case StatusRestored:
			c.Restored++
		}
	}
	return c
}
