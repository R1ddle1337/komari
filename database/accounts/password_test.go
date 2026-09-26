package accounts

import (
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
)

func TestPasswordHashesUseIndependentSaltsAndFullPassword(t *testing.T) {
	password := strings.Repeat("long-password", 10)
	a, err := hashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	b, err := hashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if a == b || !strings.HasPrefix(a, passwordPrefix) {
		t.Fatal("missing independent Argon2id salts")
	}
	if valid, legacy := verifyPassword(password, a); !valid || legacy {
		t.Fatal("correct password rejected")
	}
	if valid, _ := verifyPassword(password+"different", a); valid {
		t.Fatal("password suffix was ignored")
	}
	for _, malformed := range []string{passwordPrefix + "bad$bad", "$argon2id$v=19$m=999999999,t=999,p=255$x$y"} {
		if valid, _ := verifyPassword(password, malformed); valid {
			t.Fatal("invalid hash accepted")
		}
	}
}

func TestLegacyPasswordMigratesOnlyAfterSuccessfulVerification(t *testing.T) {
	db := dbcore.GetDBInstance()
	old := sha256.Sum256([]byte("previous-password06Wm4Jv1Hkxx"))
	user := models.User{UUID: t.Name(), Username: t.Name(), Passwd: base64.StdEncoding.EncodeToString(old[:])}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Delete(&models.User{}, "uuid = ?", user.UUID) })
	if _, ok := CheckPassword(user.Username, "incorrect"); ok {
		t.Fatal("incorrect password accepted")
	}
	var unchanged models.User
	db.Where("uuid = ?", user.UUID).First(&unchanged)
	if unchanged.Passwd != user.Passwd {
		t.Fatal("failed login changed stored password")
	}
	if id, ok := CheckPassword(user.Username, "previous-password"); !ok || id != user.UUID {
		t.Fatal("legacy login rejected")
	}
	var migrated models.User
	db.Where("uuid = ?", user.UUID).First(&migrated)
	if !strings.HasPrefix(migrated.Passwd, passwordPrefix) {
		t.Fatal("legacy password was not migrated")
	}
	if id, ok := CheckPassword(user.Username, "previous-password"); !ok || id != user.UUID {
		t.Fatal("migrated login rejected")
	}
	session := models.Session{UUID: user.UUID, Session: "password-reset-test-session", Expires: time.Now().Add(time.Hour)}
	if err := db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	if err := ForceResetPassword(user.Username, "replacement-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := GetSession(session.Session); err == nil {
		t.Fatal("password reset left a session active")
	}
	if _, ok := CheckPassword(user.Username, "previous-password"); ok {
		t.Fatal("old password still accepted")
	}
	if _, ok := CheckPassword(user.Username, "replacement-password"); !ok {
		t.Fatal("reset password rejected")
	}
}
