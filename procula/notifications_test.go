package procula

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPruneNotificationsRowCap(t *testing.T) {
	db := testDB(t)

	// Insert 1500 notifications with ascending timestamps.
	base := time.Now().UTC().Add(-10 * 24 * time.Hour)
	for i := 0; i < 1500; i++ {
		ev := NotificationEvent{
			ID:        fmt.Sprintf("notif_%05d", i),
			Timestamp: base.Add(time.Duration(i) * time.Second),
			Type:      "content_ready",
			Title:     fmt.Sprintf("Title %d", i),
			Message:   fmt.Sprintf("msg %d", i),
		}
		if err := insertNotification(db, ev); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	deleted, err := pruneNotifications(db)
	if err != nil {
		t.Fatalf("pruneNotifications: %v", err)
	}
	if deleted != 500 {
		t.Errorf("deleted = %d, want 500", deleted)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM notifications`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1000 {
		t.Errorf("remaining rows = %d, want 1000", count)
	}

	// The surviving rows must be the most-recent 1000 (IDs 500–1499).
	var minID string
	if err := db.QueryRow(`SELECT id FROM notifications ORDER BY timestamp ASC LIMIT 1`).Scan(&minID); err != nil {
		t.Fatalf("min id: %v", err)
	}
	if minID != "notif_00500" {
		t.Errorf("oldest surviving row = %q, want notif_00500", minID)
	}
}

func TestPruneNotificationsAgeCap(t *testing.T) {
	db := testDB(t)

	old := NotificationEvent{
		ID:        "old-notif",
		Timestamp: time.Now().UTC().Add(-100 * 24 * time.Hour),
		Type:      "content_ready",
		Title:     "Old Movie",
		Message:   "old",
	}
	recent := NotificationEvent{
		ID:        "recent-notif",
		Timestamp: time.Now().UTC().Add(-10 * 24 * time.Hour),
		Type:      "content_ready",
		Title:     "New Movie",
		Message:   "new",
	}

	if err := insertNotification(db, old); err != nil {
		t.Fatalf("insert old: %v", err)
	}
	if err := insertNotification(db, recent); err != nil {
		t.Fatalf("insert recent: %v", err)
	}

	deleted, err := pruneNotifications(db)
	if err != nil {
		t.Fatalf("pruneNotifications: %v", err)
	}
	if deleted != 1 {
		t.Errorf("deleted = %d, want 1", deleted)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM notifications WHERE id = 'old-notif'`).Scan(&count); err != nil {
		t.Fatalf("count old: %v", err)
	}
	if count != 0 {
		t.Errorf("old-notif still present")
	}

	if err := db.QueryRow(`SELECT COUNT(*) FROM notifications WHERE id = 'recent-notif'`).Scan(&count); err != nil {
		t.Fatalf("count recent: %v", err)
	}
	if count != 1 {
		t.Errorf("recent-notif missing")
	}
}

func TestPruneNotificationsIdempotent(t *testing.T) {
	db := testDB(t)

	// Empty table: no error, zero deleted.
	deleted, err := pruneNotifications(db)
	if err != nil {
		t.Fatalf("pruneNotifications on empty table: %v", err)
	}
	if deleted != 0 {
		t.Errorf("deleted = %d on empty table, want 0", deleted)
	}

	// Insert a single recent notification and prune — nothing qualifies.
	ev := NotificationEvent{
		ID:        "single-notif",
		Timestamp: time.Now().UTC().Add(-1 * time.Hour),
		Type:      "content_ready",
		Title:     "Movie",
		Message:   "msg",
	}
	if err := insertNotification(db, ev); err != nil {
		t.Fatalf("insert: %v", err)
	}

	deleted, err = pruneNotifications(db)
	if err != nil {
		t.Fatalf("pruneNotifications on already-pruned table: %v", err)
	}
	if deleted != 0 {
		t.Errorf("deleted = %d on already-pruned table, want 0", deleted)
	}
}

// TestSendApprise_RespectsCtxCancel verifies that sendDirect (and by the same
// code path, sendApprise) returns promptly when the context is cancelled,
// rather than blocking until the client timeout.
func TestSendApprise_RespectsCtxCancel(t *testing.T) {
	// released is closed by the test after sendDirect returns so the blocking
	// server handler can exit and httptest.Server.Close() does not hang.
	released := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Block until the client gives up OR the test signals cleanup.
		select {
		case <-r.Context().Done():
		case <-released:
		}
	}))

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	event := NotificationEvent{Title: "Test", Message: "hello", Type: "content_ready"}

	start := time.Now()
	sendDirect(ctx, srv.URL, event)
	elapsed := time.Since(start)

	// Unblock the server handler so the connection drains and srv.Close() returns.
	close(released)
	srv.Close()

	if elapsed > 2*time.Second {
		t.Errorf("sendDirect took %v after ctx cancel, want < 2s — ctx not respected", elapsed)
	}
}
