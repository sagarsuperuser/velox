package metadata

import (
	"strings"
	"testing"

	"github.com/sagarsuperuser/velox/internal/errs"
)

func TestValidate_NilAndEmpty(t *testing.T) {
	t.Parallel()
	cases := [][]byte{nil, {}, []byte("null"), []byte("{}")}
	for _, c := range cases {
		if err := Validate(c); err != nil {
			t.Errorf("%q should be valid, got %v", c, err)
		}
	}
}

func TestValidate_ValidShape(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"campaign":"summer-2025","region":"eu"}`)
	if err := Validate(raw); err != nil {
		t.Errorf("valid metadata rejected: %v", err)
	}
}

func TestValidate_NotJSONObject(t *testing.T) {
	t.Parallel()
	cases := [][]byte{
		[]byte(`"a string"`),
		[]byte(`["a","b"]`),
		[]byte(`123`),
		[]byte(`not json`),
	}
	for _, raw := range cases {
		if err := Validate(raw); err == nil {
			t.Errorf("%q should be rejected", raw)
		}
	}
}

func TestValidate_NestedValuesRejected(t *testing.T) {
	t.Parallel()
	// Arrays and objects as values would creep toward "use metadata
	// as a mini-document store" which we want to discourage at the API
	// boundary. Stripe's public contract does the same thing.
	cases := [][]byte{
		[]byte(`{"k":{"nested":"yes"}}`),
		[]byte(`{"k":[1,2,3]}`),
		[]byte(`{"k":42}`),
	}
	for _, raw := range cases {
		err := Validate(raw)
		if err == nil {
			t.Errorf("%q should be rejected", raw)
			continue
		}
		if de, ok := err.(*errs.DomainError); !ok || de.Field != "metadata" {
			t.Errorf("%q: error field: got %+v, want metadata", raw, err)
		}
	}
}

func TestValidate_TooManyKeys(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	b.WriteString(`{`)
	for i := range MaxKeys + 1 {
		if i > 0 {
			b.WriteString(`,`)
		}
		// Each value is a single char; key format deterministic.
		// rune math isn't needed, so we can use strconv-free concat.
		b.WriteString(`"k`)
		writeInt(&b, i)
		b.WriteString(`":"v"`)
	}
	b.WriteString(`}`)
	if err := Validate([]byte(b.String())); err == nil {
		t.Fatal("over-sized key count should be rejected")
	}
}

func TestValidate_KeyTooLong(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("k", MaxKeyLength+1)
	raw := []byte(`{"` + long + `":"v"}`)
	if err := Validate(raw); err == nil {
		t.Fatal("over-sized key should be rejected")
	}
}

func TestValidate_EmptyKeyRejected(t *testing.T) {
	t.Parallel()
	if err := Validate([]byte(`{"":"v"}`)); err == nil {
		t.Fatal("empty key should be rejected")
	}
}

func TestValidate_ValueTooLong(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("v", MaxValueLength+1)
	raw := []byte(`{"k":"` + long + `"}`)
	if err := Validate(raw); err == nil {
		t.Fatal("over-sized value should be rejected")
	}
}

func TestValidate_RawTooLarge(t *testing.T) {
	t.Parallel()
	big := make([]byte, MaxBytes+1)
	for i := range big {
		big[i] = 'x'
	}
	if err := Validate(big); err == nil {
		t.Fatal("oversize raw bytes should be rejected before JSON decode")
	}
}

func TestValidate_UnicodeKeyCountedInRunes(t *testing.T) {
	t.Parallel()
	// 40 two-byte runes = 80 bytes, which is within the UTF-8 rune
	// budget — should be accepted. A byte-counting implementation
	// would reject it incorrectly.
	key := strings.Repeat("é", MaxKeyLength)
	raw := []byte(`{"` + key + `":"v"}`)
	if err := Validate(raw); err != nil {
		t.Errorf("40-rune unicode key rejected: %v", err)
	}
}

// writeInt is a tiny int-to-string helper so this test doesn't pull in
// strconv just for the key generation loop.
func writeInt(b *strings.Builder, n int) {
	if n == 0 {
		b.WriteByte('0')
		return
	}
	var digits [20]byte
	i := len(digits)
	for n > 0 {
		i--
		digits[i] = byte('0' + n%10)
		n /= 10
	}
	b.Write(digits[i:])
}
