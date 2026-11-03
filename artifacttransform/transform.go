// Package artifacttransform implements a bounded, deterministic byte
// transformation engine for target-derived security artifacts. It deliberately
// has no filesystem, network, process, plugin, or arbitrary-code capability.
package artifacttransform

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"crypto/hmac"
	"crypto/sha1" // Flask's historical SecureCookieSessionInterface default.
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MaxInputBytes        = 1 << 20
	MaxSecretBytes       = 64 << 10
	MaxOperations        = 16
	MaxIntermediateBytes = 1 << 20
	MaxPreviewBytes      = 256 << 10
)

type Operation struct {
	Name      string `json:"name"`
	Algorithm string `json:"algorithm,omitempty"`
}

type Request struct {
	Operations     []Operation     `json:"operations,omitempty"`
	Recipe         string          `json:"recipe,omitempty"`
	Algorithm      string          `json:"algorithm,omitempty"`
	PayloadJSON    json.RawMessage `json:"payload_json,omitempty"`
	IssuedAtUnix   *int64          `json:"issued_at_unix,omitempty"`
	OutputEncoding string          `json:"output_encoding,omitempty"`
}

type Result struct {
	Output          string         `json:"output"`
	OutputEncoding  string         `json:"output_encoding"`
	OutputBytes     int            `json:"output_bytes"`
	OutputSHA256    string         `json:"output_sha256"`
	OutputTruncated bool           `json:"output_truncated"`
	Operations      []string       `json:"operations"`
	Metadata        map[string]any `json:"metadata,omitempty"`
}

func Execute(ctx context.Context, input, secret []byte, req Request) (Result, error) {
	if len(input) > MaxInputBytes {
		return Result{}, fmt.Errorf("input exceeds %d bytes", MaxInputBytes)
	}
	if len(secret) > MaxSecretBytes {
		return Result{}, fmt.Errorf("secret exceeds %d bytes", MaxSecretBytes)
	}
	if len(req.Operations) > MaxOperations {
		return Result{}, fmt.Errorf("operations exceeds %d", MaxOperations)
	}
	if strings.TrimSpace(req.Recipe) != "" && len(req.Operations) > 0 {
		return Result{}, errors.New("recipe and operations are mutually exclusive")
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	data := append([]byte(nil), input...)
	metadata := map[string]any{}
	audit := make([]string, 0, len(req.Operations)+1)
	var err error
	if recipe := strings.ToLower(strings.TrimSpace(req.Recipe)); recipe != "" {
		data, metadata, err = executeRecipe(ctx, recipe, input, secret, req)
		audit = append(audit, recipe)
	} else {
		for _, operation := range req.Operations {
			if err = ctx.Err(); err != nil {
				return Result{}, err
			}
			name := strings.ToLower(strings.TrimSpace(operation.Name))
			data, err = applyOperation(name, operation.Algorithm, data, secret)
			if err != nil {
				return Result{}, fmt.Errorf("%s: %w", name, err)
			}
			if len(data) > MaxIntermediateBytes {
				return Result{}, fmt.Errorf("%s output exceeds %d bytes", name, MaxIntermediateBytes)
			}
			audit = append(audit, operationAudit(name, operation.Algorithm))
		}
	}
	if err != nil {
		return Result{}, err
	}
	if len(data) > MaxIntermediateBytes {
		return Result{}, fmt.Errorf("output exceeds %d bytes", MaxIntermediateBytes)
	}
	return renderResult(data, req.OutputEncoding, audit, metadata)
}

func operationAudit(name, algorithm string) string {
	algorithm = strings.ToLower(strings.TrimSpace(algorithm))
	if algorithm == "" {
		return name
	}
	return name + ":" + algorithm
}

func applyOperation(name, algorithm string, data, secret []byte) ([]byte, error) {
	switch name {
	case "base64_encode":
		out := make([]byte, base64.StdEncoding.EncodedLen(len(data)))
		base64.StdEncoding.Encode(out, data)
		return out, nil
	case "base64_decode":
		return decodeBase64(data, false)
	case "base64url_encode":
		out := make([]byte, base64.RawURLEncoding.EncodedLen(len(data)))
		base64.RawURLEncoding.Encode(out, data)
		return out, nil
	case "base64url_decode":
		return decodeBase64(data, true)
	case "hex_encode":
		out := make([]byte, hex.EncodedLen(len(data)))
		hex.Encode(out, data)
		return out, nil
	case "hex_decode":
		out := make([]byte, hex.DecodedLen(len(bytes.TrimSpace(data))))
		n, err := hex.Decode(out, bytes.TrimSpace(data))
		return out[:n], err
	case "url_encode":
		return []byte(percentEncode(data)), nil
	case "url_decode":
		decoded, err := url.PathUnescape(string(data))
		return []byte(decoded), err
	case "gzip_compress":
		return compressGZIP(data)
	case "gzip_decompress":
		return decompressBounded(data, true)
	case "zlib_compress":
		return compressZlib(data)
	case "zlib_decompress":
		return decompressBounded(data, false)
	case "hash":
		newHash, _, err := hashAlgorithm(algorithm)
		if err != nil {
			return nil, err
		}
		h := newHash()
		_, _ = h.Write(data)
		return h.Sum(nil), nil
	case "hmac":
		if len(secret) == 0 {
			return nil, errors.New("hmac requires a non-empty secret")
		}
		newHash, _, err := hashAlgorithm(algorithm)
		if err != nil {
			return nil, err
		}
		mac := hmac.New(newHash, secret)
		_, _ = mac.Write(data)
		return mac.Sum(nil), nil
	default:
		return nil, fmt.Errorf("unsupported operation %q", name)
	}
}

func hashAlgorithm(raw string) (func() hash.Hash, string, error) {
	switch strings.ToLower(strings.ReplaceAll(strings.TrimSpace(raw), "-", "")) {
	case "sha1":
		return sha1.New, "sha1", nil
	case "sha256", "":
		return sha256.New, "sha256", nil
	case "sha512":
		return sha512.New, "sha512", nil
	default:
		return nil, "", fmt.Errorf("unsupported digest %q", raw)
	}
}

func decodeBase64(data []byte, urlSafe bool) ([]byte, error) {
	trimmed := strings.TrimSpace(string(data))
	encodings := []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding}
	if urlSafe {
		encodings = []*base64.Encoding{base64.URLEncoding, base64.RawURLEncoding}
	}
	var last error
	for _, encoding := range encodings {
		out, err := encoding.DecodeString(trimmed)
		if err == nil {
			return out, nil
		}
		last = err
	}
	return nil, last
}

func percentEncode(data []byte) string {
	const hexChars = "0123456789ABCDEF"
	var b strings.Builder
	for _, c := range data {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '.' || c == '_' || c == '~' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hexChars[c>>4])
		b.WriteByte(hexChars[c&0xf])
	}
	return b.String()
}

func compressGZIP(data []byte) ([]byte, error) {
	var out bytes.Buffer
	w := gzip.NewWriter(&out)
	if _, err := w.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func compressZlib(data []byte) ([]byte, error) {
	var out bytes.Buffer
	w := zlib.NewWriter(&out)
	if _, err := w.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func decompressBounded(data []byte, gzipMode bool) ([]byte, error) {
	var reader io.ReadCloser
	var err error
	if gzipMode {
		reader, err = gzip.NewReader(bytes.NewReader(data))
	} else {
		reader, err = zlib.NewReader(bytes.NewReader(data))
	}
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	out, err := io.ReadAll(io.LimitReader(reader, MaxIntermediateBytes+1))
	if err != nil {
		return nil, err
	}
	if len(out) > MaxIntermediateBytes {
		return nil, fmt.Errorf("decompressed output exceeds %d bytes", MaxIntermediateBytes)
	}
	return out, nil
}

func executeRecipe(ctx context.Context, recipe string, input, secret []byte, req Request) ([]byte, map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	switch recipe {
	case "jwt_inspect":
		return inspectJWT(input, secret)
	case "jwt_hs_sign":
		return signJWT(req.PayloadJSON, secret, req.Algorithm)
	case "flask_session_decode":
		return decodeFlaskSession(input, secret)
	case "flask_session_sign":
		issuedAt := time.Now().Unix()
		if req.IssuedAtUnix != nil {
			issuedAt = *req.IssuedAtUnix
		}
		return signFlaskSession(req.PayloadJSON, secret, issuedAt)
	default:
		return nil, nil, fmt.Errorf("unsupported recipe %q", recipe)
	}
}

func compactJSONObject(raw json.RawMessage, field string) ([]byte, error) {
	if len(raw) == 0 || !json.Valid(raw) {
		return nil, fmt.Errorf("%s must be valid JSON", field)
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, fmt.Errorf("%s must be a JSON object", field)
	}
	return json.Marshal(value)
}

func jwtHash(algorithm string) (func() hash.Hash, string, error) {
	switch strings.ToUpper(strings.TrimSpace(algorithm)) {
	case "HS256", "":
		return sha256.New, "HS256", nil
	case "HS384":
		return sha512.New384, "HS384", nil
	case "HS512":
		return sha512.New, "HS512", nil
	default:
		return nil, "", fmt.Errorf("JWT algorithm must be HS256, HS384, or HS512")
	}
}

func signJWT(payload json.RawMessage, secret []byte, algorithm string) ([]byte, map[string]any, error) {
	if len(secret) == 0 {
		return nil, nil, errors.New("jwt_hs_sign requires a non-empty secret")
	}
	payloadBytes, err := compactJSONObject(payload, "payload_json")
	if err != nil {
		return nil, nil, err
	}
	newHash, normalized, err := jwtHash(algorithm)
	if err != nil {
		return nil, nil, err
	}
	header, _ := json.Marshal(map[string]string{"alg": normalized, "typ": "JWT"})
	signingInput := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payloadBytes)
	mac := hmac.New(newHash, secret)
	_, _ = mac.Write([]byte(signingInput))
	token := signingInput + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return []byte(token), map[string]any{"algorithm": normalized}, nil
}

func inspectJWT(input, secret []byte) ([]byte, map[string]any, error) {
	parts := strings.Split(strings.TrimSpace(string(input)), ".")
	if len(parts) != 3 {
		return nil, nil, errors.New("JWT must contain three dot-separated segments")
	}
	header, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || !json.Valid(header) {
		return nil, nil, errors.New("JWT header is not valid base64url JSON")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !json.Valid(payload) {
		return nil, nil, errors.New("JWT payload is not valid base64url JSON")
	}
	var headerObject map[string]any
	_ = json.Unmarshal(header, &headerObject)
	algorithm, _ := headerObject["alg"].(string)
	metadata := map[string]any{
		"header_json":        string(header),
		"payload_json":       string(payload),
		"algorithm":          algorithm,
		"signature_verified": nil,
	}
	if len(secret) > 0 {
		newHash, normalized, hashErr := jwtHash(algorithm)
		if hashErr != nil {
			return nil, nil, hashErr
		}
		signature, decodeErr := base64.RawURLEncoding.DecodeString(parts[2])
		if decodeErr != nil {
			return nil, nil, errors.New("JWT signature is not valid base64url")
		}
		mac := hmac.New(newHash, secret)
		_, _ = mac.Write([]byte(parts[0] + "." + parts[1]))
		metadata["algorithm"] = normalized
		metadata["signature_verified"] = hmac.Equal(signature, mac.Sum(nil))
	}
	return payload, metadata, nil
}

func signFlaskSession(payload json.RawMessage, secret []byte, issuedAt int64) ([]byte, map[string]any, error) {
	if len(secret) == 0 {
		return nil, nil, errors.New("flask_session_sign requires a non-empty secret")
	}
	payloadBytes, err := compactJSONObject(payload, "payload_json")
	if err != nil {
		return nil, nil, err
	}
	encodedPayload, compressed, err := flaskPayload(payloadBytes)
	if err != nil {
		return nil, nil, err
	}
	timestamp := base64.RawURLEncoding.EncodeToString(intToBytes(issuedAt))
	value := encodedPayload + "." + timestamp
	signature := flaskSignature(secret, []byte(value))
	cookie := value + "." + base64.RawURLEncoding.EncodeToString(signature)
	return []byte(cookie), map[string]any{
		"compressed":     compressed,
		"issued_at_unix": issuedAt,
		"digest":         "sha1",
		"salt":           "cookie-session",
		"key_derivation": "hmac",
	}, nil
}

func flaskPayload(payload []byte) (string, bool, error) {
	var compressed bytes.Buffer
	w := zlib.NewWriter(&compressed)
	if _, err := w.Write(payload); err != nil {
		return "", false, err
	}
	if err := w.Close(); err != nil {
		return "", false, err
	}
	data := payload
	prefix := ""
	useCompressed := compressed.Len() < len(payload)-1
	if useCompressed {
		data = compressed.Bytes()
		prefix = "."
	}
	return prefix + base64.RawURLEncoding.EncodeToString(data), useCompressed, nil
}

func decodeFlaskSession(input, secret []byte) ([]byte, map[string]any, error) {
	cookie := strings.TrimSpace(string(input))
	compressed := strings.HasPrefix(cookie, ".")
	unsigned := cookie
	if compressed {
		unsigned = strings.TrimPrefix(cookie, ".")
	}
	parts := strings.Split(unsigned, ".")
	if len(parts) != 3 {
		return nil, nil, errors.New("Flask session must contain payload, timestamp, and signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, nil, errors.New("Flask payload is not valid base64url")
	}
	if compressed {
		payload, err = decompressBounded(payload, false)
		if err != nil {
			return nil, nil, fmt.Errorf("decompress Flask payload: %w", err)
		}
	}
	if !json.Valid(payload) {
		return nil, nil, errors.New("Flask payload is not valid JSON")
	}
	timestampBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(timestampBytes) == 0 || len(timestampBytes) > 8 {
		return nil, nil, errors.New("Flask timestamp is invalid")
	}
	issuedAt := bytesToInt(timestampBytes)
	metadata := map[string]any{
		"compressed":         compressed,
		"issued_at_unix":     issuedAt,
		"signature_verified": nil,
	}
	if len(secret) > 0 {
		value := cookie[:strings.LastIndex(cookie, ".")]
		signature, decodeErr := base64.RawURLEncoding.DecodeString(parts[2])
		if decodeErr != nil {
			return nil, nil, errors.New("Flask signature is not valid base64url")
		}
		metadata["signature_verified"] = hmac.Equal(signature, flaskSignature(secret, []byte(value)))
	}
	return payload, metadata, nil
}

func flaskSignature(secret, value []byte) []byte {
	derived := hmac.New(sha1.New, secret)
	_, _ = derived.Write([]byte("cookie-session"))
	mac := hmac.New(sha1.New, derived.Sum(nil))
	_, _ = mac.Write(value)
	return mac.Sum(nil)
}

func intToBytes(value int64) []byte {
	if value < 0 {
		return []byte{0}
	}
	var raw [8]byte
	binary.BigEndian.PutUint64(raw[:], uint64(value))
	index := 0
	for index < len(raw)-1 && raw[index] == 0 {
		index++
	}
	return raw[index:]
}

func bytesToInt(value []byte) int64 {
	var raw [8]byte
	copy(raw[len(raw)-len(value):], value)
	return int64(binary.BigEndian.Uint64(raw[:]))
}

func renderResult(data []byte, requested string, audit []string, metadata map[string]any) (Result, error) {
	sum := sha256.Sum256(data)
	encoding := strings.ToLower(strings.TrimSpace(requested))
	if encoding == "" || encoding == "auto" {
		if utf8.Valid(data) {
			encoding = "utf8"
		} else {
			encoding = "base64"
		}
	}
	preview := data
	truncated := len(preview) > MaxPreviewBytes
	if truncated {
		preview = preview[:MaxPreviewBytes]
	}
	var output string
	switch encoding {
	case "utf8":
		if !utf8.Valid(preview) {
			return Result{}, errors.New("output is not valid UTF-8; request base64, base64url, or hex")
		}
		output = string(preview)
	case "base64":
		output = base64.StdEncoding.EncodeToString(preview)
	case "base64url":
		output = base64.RawURLEncoding.EncodeToString(preview)
	case "hex":
		output = hex.EncodeToString(preview)
	default:
		return Result{}, fmt.Errorf("unsupported output_encoding %q", requested)
	}
	return Result{
		Output: output, OutputEncoding: encoding, OutputBytes: len(data),
		OutputSHA256: hex.EncodeToString(sum[:]), OutputTruncated: truncated,
		Operations: audit, Metadata: metadata,
	}, nil
}
