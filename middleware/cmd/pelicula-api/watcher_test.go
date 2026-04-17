package main

import (
	"testing"
	"time"
)

func TestSearchCooldown_FirstCallAllowed(t *testing.T) {
	c := newSearchCooldown()
	if !c.shouldSearch(1) {
		t.Fatal("expected first search to be allowed immediately")
	}
}

func TestSearchCooldown_SecondCallBlockedWithinCooldown(t *testing.T) {
	c := newSearchCooldown()
	c.shouldSearch(1)      // first call: allowed, records attempt=1
	if c.shouldSearch(1) { // second call: should be blocked (30min cooldown)
		t.Fatal("expected second search to be blocked within 30min cooldown")
	}
}

func TestSearchCooldown_CooldownTiersIncrease(t *testing.T) {
	c := newSearchCooldown()

	// Manually populate entries at various attempt counts to test tier lookup
	// without sleeping. Verify the tier durations are in the expected order.
	tiers := cooldownDurations
	if len(tiers) < 5 {
		t.Fatalf("expected at least 5 cooldown tiers, got %d", len(tiers))
	}
	if tiers[0] != 0 {
		t.Errorf("tier 0 should be 0 (immediate), got %v", tiers[0])
	}
	for i := 1; i < len(tiers); i++ {
		if tiers[i] <= tiers[i-1] {
			t.Errorf("tier %d (%v) should be greater than tier %d (%v)", i, tiers[i], i-1, tiers[i-1])
		}
	}
	_ = c
}

func TestSearchCooldown_ClearResetsEntry(t *testing.T) {
	c := newSearchCooldown()
	c.shouldSearch(1) // records attempt

	c.clear(1)

	if !c.shouldSearch(1) {
		t.Fatal("expected search to be allowed immediately after clear")
	}
}

func TestSearchCooldown_PastCooldownAllows(t *testing.T) {
	c := newSearchCooldown()
	c.shouldSearch(1) // attempt 1, sets lastSearched=now, next cooldown is 30min

	// Manually set lastSearched far in the past to simulate elapsed time
	c.mu.Lock()
	e := c.entries[1]
	e.lastSearched = time.Now().Add(-31 * time.Minute)
	c.entries[1] = e
	c.mu.Unlock()

	if !c.shouldSearch(1) {
		t.Fatal("expected search to be allowed after cooldown elapsed")
	}
}

func TestSearchCooldown_CapAtMaxTier(t *testing.T) {
	c := newSearchCooldown()
	maxTier := len(cooldownDurations) - 1

	// Drive attempts well past the last tier
	c.mu.Lock()
	c.entries[1] = cooldownEntry{
		lastSearched: time.Now().Add(-25 * time.Hour), // past even the 24h cap
		attempts:     maxTier + 5,
	}
	c.mu.Unlock()

	if !c.shouldSearch(1) {
		t.Fatal("expected search to be allowed after max-tier cooldown elapsed")
	}
}
