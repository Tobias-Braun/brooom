package trash

import (
	"context"
	"path/filepath"
	"testing"
)

// TestSizeHintAppliesToExactPathOnly: the size the plan measured is used for
// that path and never for another one, so a batch cannot mislabel items.
func TestSizeHintAppliesToExactPathOnly(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "a")
	b := filepath.Join(root, "b")
	writeFile(t, a, "12345", 0o644)
	writeFile(t, b, "12345", 0o644)
	ctx := WithSizeHint(context.Background(), a, 123456)
	if got, err := sizeOf(ctx, a); err != nil || got != 123456 {
		t.Errorf("hinted path: %d, %v; want the hint", got, err)
	}
	if got, err := sizeOf(ctx, b); err != nil || got != refSize(t, b) {
		t.Errorf("other path: %d, %v; want its own measurement %d", got, err, refSize(t, b))
	}
	if got, err := sizeOf(context.Background(), a); err != nil || got != refSize(t, a) {
		t.Errorf("no hint: %d, %v; want a measurement", got, err)
	}
}

// TestRemoveUsesPlannedSize: a trasher reports the planned size in its record
// (and so in the session manifest), which is what makes "reclaimed" equal the
// size the plan announced.
func TestRemoveUsesPlannedSize(t *testing.T) {
	q, root := newTestQuarantine(t, "sess")
	p := filepath.Join(root, "work", "item")
	writeFile(t, p, "12345", 0o644)
	rec, err := q.Remove(WithSizeHint(context.Background(), p, 777), p)
	if err != nil {
		t.Fatal(err)
	}
	if rec.SizeBytes != 777 {
		t.Errorf("record size = %d, want the planned 777", rec.SizeBytes)
	}
}
