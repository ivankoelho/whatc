package aitools

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// argsPurpose labels the key derived for the argument HMAC; the version lets it evolve.
const argsPurpose = "whatc/ai_tools/args/v1"

const (
	maxArgKeys   = 20
	maxArgKeyLen = 64
)

// CanonicalJSON re-encodes a JSON document so the same information always gives the same bytes:
// object keys sorted recursively, no insignificant whitespace, numbers kept exactly as written
// (1.0 stays 1.0), UTF-8 and no HTML escaping. It fails for input that is not valid JSON.
func CanonicalJSON(raw []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil { // map keys are sorted by encoding/json
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// ArgsHMAC is the keyed fingerprint of a call's arguments: HMAC-SHA256 over their canonical form,
// with a key derived from the server secret and the purpose label. A plain SHA-256 of a CPF, a
// phone number or a short id can be brute-forced offline; the keyed one cannot without the secret.
// It exists to see that two calls had the same arguments, not to recover them. With no secret it
// returns "" (it never falls back to an unkeyed hash).
func ArgsHMAC(secret string, args []byte) string {
	if secret == "" {
		return ""
	}
	canon, err := CanonicalJSON(args)
	if err != nil {
		canon = args // still keyed: an unreadable document is fingerprinted as it came
	}
	return hex.EncodeToString(mac(derivedKey(secret), canon))
}

func derivedKey(secret string) []byte { return derivedKeyFor(secret, argsPurpose) }

// derivedKeyFor derives a purpose-specific key from the server secret, so no key is reused across uses.
func derivedKeyFor(secret, purpose string) []byte { return mac([]byte(secret), []byte(purpose)) }

func mac(key, msg []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(msg)
	return m.Sum(nil)
}

// ArgsKeys lists the top-level key names of the arguments (never values), sorted and bounded.
func ArgsKeys(args []byte) []string {
	var obj map[string]json.RawMessage
	if json.Unmarshal(args, &obj) != nil {
		return nil
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		if len(k) > maxArgKeyLen {
			k = k[:maxArgKeyLen]
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > maxArgKeys {
		keys = keys[:maxArgKeys]
	}
	return keys
}
