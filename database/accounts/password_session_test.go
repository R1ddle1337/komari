package accounts

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
)

func TestPasswordSessionProofRejectsPasswordReset(t *testing.T) {
	for _, method := range []string{"force_reset", "update_user"} {
		t.Run(method, func(t *testing.T) {
			u, err := CreateAccount(t.Name(), "original-password")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { DeleteAccountByUsername(u.Username) })
			id, proof, ok := CheckPasswordWithProof(u.Username, "original-password")
			if !ok || id != u.UUID || proof == "" {
				t.Fatal("valid password did not produce a proof")
			}
			session, err := CreatePasswordSession(id, proof, 600, "proof-test", "127.0.0.1")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { DeleteSession(session) })
			if actual, err := GetSession(session); err != nil || actual != u.UUID {
				t.Fatalf("valid proof session rejected: %v", err)
			}
			password := "replacement-password"
			if method == "force_reset" {
				err = ForceResetPassword(u.Username, password)
			} else {
				err = UpdateUser(u.UUID, nil, &password, nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := GetSession(session); err == nil {
				t.Fatal("password reset retained a previously created session")
			}
			if token, err := CreatePasswordSession(id, proof, 600, "test", "127.0.0.1"); token != "" || !errors.Is(err, ErrCredentialsChanged) {
				t.Fatalf("old proof created a session after reset: error=%v", err)
			}
			newID, newProof, ok := CheckPasswordWithProof(u.Username, password)
			if !ok || newID != u.UUID || newProof == proof {
				t.Fatal("reset password did not produce a new proof")
			}
			newSession, err := CreatePasswordSession(newID, newProof, 600, "test", "127.0.0.1")
			if err != nil {
				t.Fatalf("new proof rejected: %v", err)
			}
			t.Cleanup(func() { DeleteSession(newSession) })
		})
	}
}

func TestLegacyPasswordProofUsesMigratedHash(t *testing.T) {
	const password = "legacy-password"
	old := sha256.Sum256([]byte(password + "06Wm4Jv1Hkxx"))
	u := models.User{UUID: t.Name(), Username: t.Name(), Passwd: base64.StdEncoding.EncodeToString(old[:])}
	db := dbcore.GetDBInstance()
	if err := db.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { DeleteAccountByUsername(u.Username) })
	id, proof, ok := CheckPasswordWithProof(u.Username, password)
	if !ok || id != u.UUID || proof == u.Passwd || !strings.HasPrefix(proof, passwordPrefix) {
		t.Fatal("legacy verification did not return migrated proof")
	}
	current, err := GetUserByUUID(id)
	if err != nil || current.Passwd != proof {
		t.Fatalf("proof differs from stored migrated hash: %v", err)
	}
	session, err := CreatePasswordSession(id, proof, 600, "migration-test", "127.0.0.1")
	if err != nil {
		t.Fatalf("migrated proof could not create session: %v", err)
	}
	t.Cleanup(func() { DeleteSession(session) })
	if token, err := CreatePasswordSession(id, u.Passwd, 600, "test", "127.0.0.1"); token != "" || !errors.Is(err, ErrCredentialsChanged) {
		t.Fatalf("obsolete legacy proof accepted: %v", err)
	}
}

func TestPasswordChangeRollsBackIfSessionRevocationFails(t *testing.T) {
	for _, method := range []string{"force_reset", "update_user"} {
		t.Run(method, func(t *testing.T) {
			u, err := CreateAccount(t.Name(), "original-password")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { DeleteAccountByUsername(u.Username) })
			session, err := CreatePasswordSession(u.UUID, u.Passwd, 600, "rollback-test", "127.0.0.1")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { DeleteSession(session) })
			db := dbcore.GetDBInstance()
			if err := db.Exec(`CREATE TEMP TRIGGER deny_session_revocation BEFORE DELETE ON sessions BEGIN SELECT RAISE(ABORT, 'test session revocation failure'); END`).Error; err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Exec("DROP TRIGGER IF EXISTS deny_session_revocation") })
			password := "replacement-password"
			if method == "force_reset" {
				err = ForceResetPassword(u.Username, password)
			} else {
				err = UpdateUser(u.UUID, nil, &password, nil)
			}
			if err == nil {
				t.Fatal("failed session revocation reported success")
			}
			current, err := GetUserByUUID(u.UUID)
			if err != nil || current.Passwd != u.Passwd {
				t.Fatalf("password update committed without session revocation: %v", err)
			}
			if _, err := GetSession(session); err != nil {
				t.Fatalf("rollback lost the existing session: %v", err)
			}
		})
	}
}

func TestPasswordProofCannotCreateSessionForDeletedAccount(t *testing.T) {
	u, err := CreateAccount(t.Name(), "original-password")
	if err != nil {
		t.Fatal(err)
	}
	if err := DeleteAccountByUsername(u.Username); err != nil {
		t.Fatal(err)
	}
	if token, err := CreatePasswordSession(u.UUID, u.Passwd, 600, "test", "127.0.0.1"); token != "" || !errors.Is(err, ErrCredentialsChanged) {
		t.Fatalf("deleted account proof accepted: %v", err)
	}
}
