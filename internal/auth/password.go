package auth

import (
	"golang.org/x/crypto/bcrypt"
)

// HashPassword returns a bcrypt hash of the password.
func HashPassword(password string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(h), err
}

// CheckPassword reports whether password matches the stored hash.
func CheckPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// dummyHash is a valid bcrypt hash used to equalize the login path when the
// username does not exist. It is never matched against a real account.
var dummyHash = func() string {
	h, _ := bcrypt.GenerateFromPassword([]byte("nanoflux-dummy-password"), bcrypt.DefaultCost)
	return string(h)
}()

// CheckDummyPassword burns the same bcrypt work as a real failed login. The
// result is intentionally discarded; callers use it only to avoid a timing
// oracle that reveals whether an account exists.
func CheckDummyPassword(password string) {
	_ = bcrypt.CompareHashAndPassword([]byte(dummyHash), []byte(password))
}
