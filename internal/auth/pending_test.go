package auth

import (
	"testing"
	"time"
)

func TestPendingIsSingleUseAndExpires(t *testing.T) {
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	p := NewPendings(func() time.Time { return now })
	tok, _ := p.Start("u1")
	if id, ok := p.Peek(tok); !ok || id != "u1" {
		t.Fatalf("peek: %q %v", id, ok)
	}
	if _, ok := p.Peek("nope"); ok {
		t.Error("an unknown token was live")
	}
	if id, ok := p.Finish(tok); !ok || id != "u1" {
		t.Fatalf("finish: %q %v", id, ok)
	}
	if _, ok := p.Finish(tok); ok {
		t.Error("a token worked twice")
	}
	tok2, _ := p.Start("u1")
	now = now.Add(PendingTTL + time.Second)
	if _, ok := p.Peek(tok2); ok {
		t.Error("an expired token was live")
	}
	tok3, _ := p.Start("u2")
	p.DropUser("u2")
	if _, ok := p.Peek(tok3); ok {
		t.Error("a dropped user's token was live")
	}
}

func TestSecondStepLockStartsAtFiveEndsAfterTenMinutesAndResetsOnSuccess(t *testing.T) {
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	p := NewPendings(func() time.Time { return now })
	for i := 1; i <= 4; i++ {
		if p.Fail("u") || p.Locked("u") {
			t.Fatalf("locked after %d wrong codes", i)
		}
	}
	if !p.Fail("u") || !p.Locked("u") {
		t.Fatal("the fifth wrong code did not lock")
	}
	if p.Fail("u") {
		t.Error("a second lock event was reported while locked")
	}
	if p.Locked("other") {
		t.Error("the lock leaked to another account")
	}
	now = now.Add(CodeLockFor + time.Second)
	if p.Locked("u") {
		t.Error("still locked after ten minutes")
	}
	// A good sign-in forgets the count; slow wrong codes spread over more than the window never lock.
	p.Fail("v")
	p.Fail("v")
	p.Clear("v")
	for i := 0; i < 4; i++ {
		p.Fail("v")
	}
	if p.Locked("v") {
		t.Error("the count was not cleared")
	}
	for i := 0; i < 8; i++ {
		now = now.Add(CodeFailWindow/2 + time.Second)
		if p.Fail("w") {
			t.Fatal("slow guesses locked the account")
		}
	}
}
