package accounts

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	passwordPrefix    = "$argon2id$v=19$m=65536,t=2,p=1$"
	passwordSaltBytes = 16
	passwordKeyBytes  = 32
)

// Keep the work factor fixed when reading stored hashes, so malformed storage
// cannot request unbounded CPU or memory. New accounts and password changes use
// independent random salts; the old fixed-salt format is read only for migration.
func passwordWork(password string, salt []byte) []byte {
	return argon2.IDKey([]byte(password), salt, 2, 64*1024, 1, passwordKeyBytes)
}

func hashPassword(password string) (string, error) {
	salt := make([]byte, passwordSaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := passwordWork(password, salt)
	return passwordPrefix + base64.RawStdEncoding.EncodeToString(salt) + "$" + base64.RawStdEncoding.EncodeToString(key), nil
}

func verifyPassword(password, encoded string) (valid, legacy bool) {
	if strings.HasPrefix(encoded, passwordPrefix) {
		parts := strings.Split(strings.TrimPrefix(encoded, passwordPrefix), "$")
		if len(parts) != 2 {
			return false, false
		}
		salt, saltErr := base64.RawStdEncoding.Strict().DecodeString(parts[0])
		key, keyErr := base64.RawStdEncoding.Strict().DecodeString(parts[1])
		if saltErr != nil || keyErr != nil || len(salt) != passwordSaltBytes || len(key) != passwordKeyBytes {
			return false, false
		}
		return subtle.ConstantTimeCompare(passwordWork(password, salt), key) == 1, false
	}
	old := sha256.Sum256([]byte(password + "06Wm4Jv1Hkxx"))
	legacyHash := base64.StdEncoding.EncodeToString(old[:])
	if subtle.ConstantTimeCompare([]byte(encoded), []byte(legacyHash)) == 1 {
		return true, true
	}
	// Failed legacy and unknown accounts should not identify their hash format
	// through a much faster password failure than modern accounts.
	passwordWork(password, make([]byte, passwordSaltBytes))
	return false, false
}
