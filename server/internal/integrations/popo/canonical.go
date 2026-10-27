package popo

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

const (
	OutboundHashAlgorithm = "multica-outbound-sha256-v2"
	InboundHashAlgorithm  = "multica-inbound-sha256-v1"
)

var ErrCanonicalJSON = errors.New("popo: unsupported canonical JSON")

func CanonicalJSON(value any) (string, error) {
	var out strings.Builder
	if err := appendCanonicalJSON(&out, value); err != nil {
		return "", err
	}
	return out.String(), nil
}

func DecodeCanonicalJSON(raw []byte) (any, error) {
	if !utf8.Valid(raw) {
		return nil, fmt.Errorf("%w: invalid UTF-8", ErrCanonicalJSON)
	}
	if err := validateJSONUnicodeEscapes(raw); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCanonicalJSON, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("%w: multiple JSON values", ErrCanonicalJSON)
	}
	return value, nil
}

func HashCanonicalOutbound(canonicalJSON string) string {
	return canonicalHash(OutboundHashAlgorithm, canonicalJSON)
}

func HashCanonicalInbound(canonicalJSON string) string {
	return canonicalHash(InboundHashAlgorithm, canonicalJSON)
}

func canonicalHash(domain, canonicalJSON string) string {
	digest := sha256.Sum256([]byte(domain + "\n" + canonicalJSON))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func appendCanonicalJSON(out *strings.Builder, value any) error {
	switch typed := value.(type) {
	case nil:
		out.WriteString("null")
		return nil
	case bool:
		if typed {
			out.WriteString("true")
		} else {
			out.WriteString("false")
		}
		return nil
	case string:
		return appendCanonicalString(out, typed)
	case json.Number:
		number, err := canonicalNumberString(string(typed), true)
		if err != nil {
			return err
		}
		out.WriteString(number)
		return nil
	case float64:
		return appendCanonicalFloat(out, typed)
	case float32:
		return appendCanonicalFloat(out, float64(typed))
	case int:
		return appendCanonicalFloat(out, float64(typed))
	case int8:
		return appendCanonicalFloat(out, float64(typed))
	case int16:
		return appendCanonicalFloat(out, float64(typed))
	case int32:
		return appendCanonicalFloat(out, float64(typed))
	case int64:
		return appendCanonicalFloat(out, float64(typed))
	case uint:
		return appendCanonicalFloat(out, float64(typed))
	case uint8:
		return appendCanonicalFloat(out, float64(typed))
	case uint16:
		return appendCanonicalFloat(out, float64(typed))
	case uint32:
		return appendCanonicalFloat(out, float64(typed))
	case uint64:
		return appendCanonicalFloat(out, float64(typed))
	case []any:
		out.WriteByte('[')
		for index, item := range typed {
			if index > 0 {
				out.WriteByte(',')
			}
			if err := appendCanonicalJSON(out, item); err != nil {
				return fmt.Errorf("array index %d: %w", index, err)
			}
		}
		out.WriteByte(']')
		return nil
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			if !utf8.ValidString(key) {
				return fmt.Errorf("%w: invalid object key UTF-8", ErrCanonicalJSON)
			}
			keys = append(keys, key)
		}
		sortUTF16Strings(keys)
		out.WriteByte('{')
		for index, key := range keys {
			if index > 0 {
				out.WriteByte(',')
			}
			if err := appendCanonicalString(out, key); err != nil {
				return err
			}
			out.WriteByte(':')
			if err := appendCanonicalJSON(out, typed[key]); err != nil {
				return fmt.Errorf("object key %q: %w", key, err)
			}
		}
		out.WriteByte('}')
		return nil
	default:
		return fmt.Errorf("%w: %T", ErrCanonicalJSON, value)
	}
}

func appendCanonicalFloat(out *strings.Builder, value float64) error {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return fmt.Errorf("%w: non-finite number", ErrCanonicalJSON)
	}
	if value == 0 {
		out.WriteByte('0')
		return nil
	}
	absolute := math.Abs(value)
	if absolute >= 1e-6 && absolute < 1e21 {
		out.WriteString(strconv.FormatFloat(value, 'f', -1, 64))
		return nil
	}
	formatted := strconv.FormatFloat(value, 'g', -1, 64)
	if exponent := strings.LastIndexByte(formatted, 'e'); exponent >= 0 {
		mantissa, exponentValue := formatted[:exponent], formatted[exponent+1:]
		sign := "+"
		if strings.HasPrefix(exponentValue, "-") {
			sign = "-"
			exponentValue = strings.TrimPrefix(exponentValue, "-")
		} else {
			exponentValue = strings.TrimPrefix(exponentValue, "+")
		}
		exponentValue = strings.TrimLeft(exponentValue, "0")
		if exponentValue == "" {
			exponentValue = "0"
		}
		formatted = mantissa + "e" + sign + exponentValue
	}
	out.WriteString(formatted)
	return nil
}

func canonicalNumberString(raw string, validateJSON bool) (string, error) {
	if validateJSON && !json.Valid([]byte(raw)) {
		return "", fmt.Errorf("%w: invalid number %q", ErrCanonicalJSON, raw)
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return "", fmt.Errorf("%w: number %q", ErrCanonicalJSON, raw)
	}
	var out strings.Builder
	if err := appendCanonicalFloat(&out, value); err != nil {
		return "", err
	}
	return out.String(), nil
}

func appendCanonicalString(out *strings.Builder, value string) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("%w: invalid string UTF-8", ErrCanonicalJSON)
	}
	const hexadecimal = "0123456789abcdef"
	out.WriteByte('"')
	for _, r := range value {
		switch r {
		case '"', '\\':
			out.WriteByte('\\')
			out.WriteRune(r)
		case '\b':
			out.WriteString(`\b`)
		case '\f':
			out.WriteString(`\f`)
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		case '\t':
			out.WriteString(`\t`)
		default:
			if r < 0x20 {
				out.WriteString(`\u00`)
				out.WriteByte(hexadecimal[byte(r)>>4])
				out.WriteByte(hexadecimal[byte(r)&0x0f])
			} else {
				out.WriteRune(r)
			}
		}
	}
	out.WriteByte('"')
	return nil
}

func sortUTF16Strings(values []string) {
	compare := func(left, right string) int {
		leftUnits, rightUnits := utf16.Encode([]rune(left)), utf16.Encode([]rune(right))
		for index := 0; index < len(leftUnits) && index < len(rightUnits); index++ {
			if leftUnits[index] < rightUnits[index] {
				return -1
			}
			if leftUnits[index] > rightUnits[index] {
				return 1
			}
		}
		switch {
		case len(leftUnits) < len(rightUnits):
			return -1
		case len(leftUnits) > len(rightUnits):
			return 1
		default:
			return 0
		}
	}
	for index := 1; index < len(values); index++ {
		for cursor := index; cursor > 0 && compare(values[cursor], values[cursor-1]) < 0; cursor-- {
			values[cursor], values[cursor-1] = values[cursor-1], values[cursor]
		}
	}
}

func validateJSONUnicodeEscapes(raw []byte) error {
	inString := false
	for index := 0; index < len(raw); index++ {
		switch raw[index] {
		case '"':
			inString = !inString
			continue
		case '\\':
			if !inString {
				continue
			}
		default:
			continue
		}
		index++
		if index >= len(raw) || raw[index] != 'u' {
			continue
		}
		if index+4 >= len(raw) {
			return fmt.Errorf("%w: truncated Unicode escape", ErrCanonicalJSON)
		}
		code, ok := parseHex16(raw[index+1 : index+5])
		if !ok {
			return fmt.Errorf("%w: invalid Unicode escape", ErrCanonicalJSON)
		}
		index += 4
		switch {
		case code >= 0xd800 && code <= 0xdbff:
			if index+6 >= len(raw) || raw[index+1] != '\\' || raw[index+2] != 'u' {
				return fmt.Errorf("%w: unpaired high surrogate", ErrCanonicalJSON)
			}
			low, valid := parseHex16(raw[index+3 : index+7])
			if !valid || low < 0xdc00 || low > 0xdfff {
				return fmt.Errorf("%w: unpaired high surrogate", ErrCanonicalJSON)
			}
			index += 6
		case code >= 0xdc00 && code <= 0xdfff:
			return fmt.Errorf("%w: unpaired low surrogate", ErrCanonicalJSON)
		}
	}
	return nil
}

func parseHex16(raw []byte) (uint16, bool) {
	if len(raw) != 4 {
		return 0, false
	}
	var value uint16
	for _, char := range raw {
		value <<= 4
		switch {
		case char >= '0' && char <= '9':
			value |= uint16(char - '0')
		case char >= 'a' && char <= 'f':
			value |= uint16(char-'a') + 10
		case char >= 'A' && char <= 'F':
			value |= uint16(char-'A') + 10
		default:
			return 0, false
		}
	}
	return value, true
}
