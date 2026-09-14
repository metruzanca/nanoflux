package auth

import "testing"

func TestPassword(t *testing.T) {
	hash, err := HashPassword("correct horse")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if hash == "correct horse" {
		t.Fatal("hash must not equal plaintext")
	}
	if !CheckPassword(hash, "correct horse") {
		t.Fatal("expected password to match")
	}
	if CheckPassword(hash, "wrong") {
		t.Fatal("expected wrong password to fail")
	}
}
