package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestPassword(t *testing.T) {
	h, err := HashPassword("123456")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$") {
		t.Fatalf("formato: %s", h)
	}
	if ok, _ := VerifyPassword("123456", h); !ok {
		t.Error("el PIN correcto no verifica")
	}
	if ok, _ := VerifyPassword("123457", h); ok {
		t.Error("un PIN incorrecto verifica")
	}
}

func TestTokens(t *testing.T) {
	tk := NewTokens([]byte(strings.Repeat("k", 32)))
	raw, _, err := tk.Issue(Claims{Role: RoleAdmin, RegisteredClaims: Subject("1")}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if c, err := tk.Parse(raw); err != nil || c.Role != RoleAdmin || c.Subject != "1" {
		t.Fatalf("token válido rechazado: %v", err)
	}

	// Other key, other algorithm, "none" and expired tokens are all rejected.
	other, _, _ := NewTokens([]byte(strings.Repeat("x", 32))).Issue(Claims{Role: RoleAdmin, RegisteredClaims: Subject("1")}, time.Minute)
	hs512, _ := jwt.NewWithClaims(jwt.SigningMethodHS512, Claims{Role: RoleAdmin, RegisteredClaims: jwt.RegisteredClaims{
		Subject: "1", Issuer: issuer, ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute))}}).SignedString([]byte(strings.Repeat("k", 32)))
	none, _ := jwt.NewWithClaims(jwt.SigningMethodNone, Claims{Role: RoleAdmin, RegisteredClaims: jwt.RegisteredClaims{
		Subject: "1", Issuer: issuer, ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute))}}).SignedString(jwt.UnsafeAllowNoneSignatureType)
	expired, _, _ := tk.Issue(Claims{Role: RoleAdmin, RegisteredClaims: Subject("1")}, -time.Hour)
	for name, bad := range map[string]string{"otra llave": other, "HS512": hs512, "none": none, "vencido": expired} {
		if _, err := tk.Parse(bad); err == nil {
			t.Errorf("%s: debería rechazarse", name)
		}
	}
}
