package popo

import (
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"
)

type canonicalGoldenFixture struct {
	HashAlgorithm string `json:"hash_algorithm"`
	Vectors       []struct {
		Name        string          `json:"name"`
		Command     json.RawMessage `json:"command"`
		Projection  json.RawMessage `json:"projection"`
		Canonical   string          `json:"canonical"`
		PayloadHash string          `json:"payload_hash"`
	} `json:"vectors"`
}

func readCanonicalGolden(t *testing.T, name string) canonicalGoldenFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var fixture canonicalGoldenFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Vectors) == 0 {
		t.Fatal("golden fixture has no vectors")
	}
	return fixture
}

func TestCanonicalOutboundAllGoldenVectors(t *testing.T) {
	fixture := readCanonicalGolden(t, "canonical-outbound.json")
	if fixture.HashAlgorithm != OutboundHashAlgorithm {
		t.Fatalf("algorithm = %s", fixture.HashAlgorithm)
	}
	for _, vector := range fixture.Vectors {
		t.Run(vector.Name, func(t *testing.T) {
			canonical, hash, err := OutboundCommandHash(vector.Command)
			if err != nil {
				t.Fatal(err)
			}
			if canonical != vector.Canonical || hash != vector.PayloadHash {
				t.Fatalf("canonical/hash mismatch\n got: %s %s\nwant: %s %s", canonical, hash, vector.Canonical, vector.PayloadHash)
			}
			if strings.Contains(canonical, "lease-not-hashed") || strings.Contains(canonical, "trace-not-hashed") || strings.Contains(canonical, "created_at") {
				t.Fatalf("mutable envelope fields leaked into canonical: %s", canonical)
			}
			decoded, err := DecodeCanonicalJSON(vector.Command)
			if err != nil {
				t.Fatal(err)
			}
			projection, err := CanonicalOutboundProjection(decoded.(map[string]any))
			if err != nil {
				t.Fatal(err)
			}
			projectedCanonical, err := CanonicalJSON(projection)
			if err != nil || projectedCanonical != vector.Canonical {
				t.Fatalf("projection = %s, %v", projectedCanonical, err)
			}
		})
	}
}

func TestCanonicalInboundAllGoldenVectors(t *testing.T) {
	fixture := readCanonicalGolden(t, "canonical-inbound.json")
	if fixture.HashAlgorithm != InboundHashAlgorithm {
		t.Fatalf("algorithm = %s", fixture.HashAlgorithm)
	}
	for _, vector := range fixture.Vectors {
		t.Run(vector.Name, func(t *testing.T) {
			projection, err := DecodeCanonicalJSON(vector.Projection)
			if err != nil {
				t.Fatal(err)
			}
			canonical, err := CanonicalJSON(projection)
			if err != nil {
				t.Fatal(err)
			}
			if canonical != vector.Canonical {
				t.Fatalf("canonical\n got: %s\nwant: %s", canonical, vector.Canonical)
			}
			if hash := HashCanonicalInbound(canonical); hash != vector.PayloadHash {
				t.Fatalf("hash = %s, want %s", hash, vector.PayloadHash)
			}
		})
	}
}

func TestCanonicalJSONJavaScriptNumberAndStringSemantics(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want string
	}{
		{raw: "1.0", want: "1"},
		{raw: "-0.0", want: "0"},
		{raw: "0.000001", want: "0.000001"},
		{raw: "0.0000001", want: "1e-7"},
		{raw: "100000000000000000000", want: "100000000000000000000"},
		{raw: "1e21", want: "1e+21"},
		{raw: "1000000000000000100", want: "1000000000000000100"},
	} {
		value, err := DecodeCanonicalJSON([]byte(tc.raw))
		if err != nil {
			t.Fatalf("decode %s: %v", tc.raw, err)
		}
		canonical, err := CanonicalJSON(value)
		if err != nil || canonical != tc.want {
			t.Errorf("%s = %s, %v; want %s", tc.raw, canonical, err, tc.want)
		}
	}
	canonical, err := CanonicalJSON(map[string]any{"html": "<>&雪", "control": "\x00\n"})
	if err != nil {
		t.Fatal(err)
	}
	if canonical != `{"control":"\u0000\n","html":"<>&雪"}` {
		t.Fatalf("string canonical = %s", canonical)
	}
	if HashCanonicalInbound(`{"a":1}`) == HashCanonicalOutbound(`{"a":1}`) {
		t.Fatal("domain separators produced identical hashes")
	}
}

func TestCanonicalJSONFailsClosed(t *testing.T) {
	for _, raw := range []string{`1e400`, `"\ud800"`, `"\udc00"`} {
		value, err := DecodeCanonicalJSON([]byte(raw))
		if err == nil {
			_, err = CanonicalJSON(value)
		}
		if err == nil {
			t.Errorf("%s was accepted", raw)
		}
	}
	for _, value := range []any{math.NaN(), math.Inf(1), map[string]any{"fn": func() {}}} {
		if _, err := CanonicalJSON(value); err == nil {
			t.Errorf("%T was accepted", value)
		}
	}
}
