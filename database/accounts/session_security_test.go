package accounts

import (
	"errors"
	"testing"
	"time"

	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
)

func TestGetUserBySessionRejectsExpiredAndDeletedSessions(t *testing.T) {
	user, err := CreateAccount("session-expiry-test", "test-only-password")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { DeleteAccountByUsername(user.Username) })
	session, err := CreateSession(user.UUID, 600, "test", "127.0.0.1", "password")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { DeleteSession(session) })
	if got, err := GetUserBySession(session); err != nil || got.UUID != user.UUID {
		t.Fatalf("valid session rejected: %v", err)
	}
	if err := dbcore.GetDBInstance().Model(&models.Session{}).Where("session = ?", session).Update("expires", time.Now().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := GetUserBySession(session); err == nil {
		t.Fatal("expired session accepted")
	}
	if _, err := GetUserBySession(session); err == nil {
		t.Fatal("deleted expired session accepted")
	}
}

func TestEnableTwoFactorCannotReplaceExistingSecret(t *testing.T) {
	user, err := CreateAccount("two-factor-replace-test", "test-only-password")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { DeleteAccountByUsername(user.Username) })
	if err := Enable2Fa(user.UUID, "original-secret"); err != nil {
		t.Fatal(err)
	}
	if err := Enable2Fa(user.UUID, "replacement-secret"); !errors.Is(err, ErrTwoFactorAlreadyEnabled) {
		t.Fatalf("replacement error=%v", err)
	}
	got, err := GetUserByUUID(user.UUID)
	if err != nil || got.TwoFactor != "original-secret" {
		t.Fatalf("original factor was changed: %v", err)
	}
	if err := Disable2Fa(user.UUID); err != nil {
		t.Fatal(err)
	}
	if err := Enable2Fa(user.UUID, "replacement-secret"); err != nil {
		t.Fatalf("enabling after explicit disable failed: %v", err)
	}
}

func TestGetSessionRejectsDeletedAccountWithOrphanSession(t *testing.T) {
	u, err := CreateAccount("orphan-session-test", "test-only-password")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { DeleteAccountByUsername(u.Username) })
	session, err := CreateSession(u.UUID, 600, "test", "127.0.0.1", "password")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { DeleteSession(session) })
	db := dbcore.GetDBInstance()
	var foreignKeys int
	if err := db.Raw("PRAGMA foreign_keys").Scan(&foreignKeys).Error; err != nil {
		t.Fatal(err)
	}
	// Model a legacy/imported orphan without relying on cascade constraints.
	if err := db.Exec("PRAGMA foreign_keys = OFF").Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if foreignKeys != 0 {
			db.Exec("PRAGMA foreign_keys = ON")
		}
	})
	if err := DeleteAccountByUsername(u.Username); err != nil {
		t.Fatal(err)
	}
	if foreignKeys != 0 {
		if err := db.Exec("PRAGMA foreign_keys = ON").Error; err != nil {
			t.Fatal(err)
		}
	}
	var count int64
	if err := db.Model(&models.Session{}).Where("session = ?", session).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("orphan fixture missing: count=%d error=%v", count, err)
	}
	if _, err := GetSession(session); err == nil {
		t.Fatal("orphan session authenticated a deleted account")
	}
}
