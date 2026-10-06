package hub

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"
)

// argon2id parameters. Variables so tests can lower the cost; production uses
// the defaults (64 MiB, 3 passes), per the OWASP guidance for argon2id.
var (
	argonTime    uint32 = 3
	argonMemory  uint32 = 64 * 1024
	argonThreads uint8  = 2
)

const (
	argonKeyLen  = 32
	argonSaltLen = 16
	minPassword  = 12
	maxPassword  = 256
)

// hashSlots bounds concurrent hashing: each one needs argonMemory of RAM, and
// the login endpoint is reachable by anyone.
var hashSlots = make(chan struct{}, 2)

// HashPassword returns an encoded argon2id hash: $argon2id$v=19$m=..,t=..,p=..$salt$key.
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	hashSlots <- struct{}{}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	<-hashSlots
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

func parseHash(encoded string) (salt, key []byte, m, t uint32, p uint8, err error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return nil, nil, 0, 0, 0, errors.New("not an argon2id hash")
	}
	var v int
	if _, err = fmt.Sscanf(parts[2], "v=%d", &v); err != nil || v != argon2.Version {
		return nil, nil, 0, 0, 0, errors.New("bad argon2 version")
	}
	var pp uint32
	if _, err = fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &pp); err != nil || pp == 0 || pp > 255 {
		return nil, nil, 0, 0, 0, errors.New("bad argon2 parameters")
	}
	if salt, err = base64.RawStdEncoding.DecodeString(parts[4]); err != nil {
		return nil, nil, 0, 0, 0, err
	}
	if key, err = base64.RawStdEncoding.DecodeString(parts[5]); err != nil || len(key) == 0 {
		return nil, nil, 0, 0, 0, errors.New("bad argon2 key")
	}
	return salt, key, m, t, uint8(pp), nil
}

// VerifyPassword compares in constant time. It runs the full hash even for a
// malformed encoding, so a bad row costs the same as a wrong password.
func VerifyPassword(encoded, password string) bool {
	salt, key, m, t, p, err := parseHash(encoded)
	if err != nil {
		VerifyPasswordDummy(password)
		return false
	}
	// Refuse parameters far beyond ours: the hash column is trusted, but a
	// restored or tampered database must not make login allocate gigabytes.
	if m > 4*argonMemory || t > 4*argonTime {
		return false
	}
	hashSlots <- struct{}{}
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(key)))
	<-hashSlots
	return subtle.ConstantTimeCompare(got, key) == 1
}

var (
	dummyOnce sync.Once
	dummyHash string
)

// VerifyPasswordDummy spends the time of one verification. Login calls it for
// unknown names so the response time does not reveal which names exist.
func VerifyPasswordDummy(password string) {
	dummyOnce.Do(func() { dummyHash, _ = HashPassword("handloom-dummy-password") })
	salt, key, m, t, p, err := parseHash(dummyHash)
	if err != nil {
		return
	}
	hashSlots <- struct{}{}
	argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(key)))
	<-hashSlots
}

// CheckPassword applies the password policy.
func CheckPassword(name, password string) error {
	switch {
	case len(password) < minPassword:
		return fmt.Errorf("the password needs at least %d characters", minPassword)
	case len(password) > maxPassword:
		return fmt.Errorf("the password is longer than %d characters", maxPassword)
	case strings.EqualFold(password, name):
		return errors.New("the password must not be the name")
	}
	return nil
}
