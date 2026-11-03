package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nobanic/UroborOS/artifacttransform"
	"github.com/nobanic/norma/permission"
	actool "github.com/nobanic/norma/tool"
)

type artifactSource struct {
	Literal   *string `json:"literal,omitempty"`
	Base64    *string `json:"base64,omitempty"`
	TrafficID string  `json:"traffic_id,omitempty"`
	Side      string  `json:"side,omitempty"`
	Offset    int64   `json:"offset,omitempty"`
	Length    int64   `json:"length,omitempty"`
}

type artifactTransformArgs struct {
	Input          *artifactSource               `json:"input,omitempty"`
	Secret         *artifactSource               `json:"secret,omitempty"`
	Operations     []artifacttransform.Operation `json:"operations,omitempty"`
	Recipe         string                        `json:"recipe,omitempty"`
	Algorithm      string                        `json:"algorithm,omitempty"`
	PayloadJSON    json.RawMessage               `json:"payload_json,omitempty"`
	IssuedAtUnix   *int64                        `json:"issued_at_unix,omitempty"`
	OutputEncoding string                        `json:"output_encoding,omitempty"`
}

type artifactProvenance struct {
	Kind        string `json:"kind"`
	TrafficID   string `json:"traffic_id,omitempty"`
	Side        string `json:"side,omitempty"`
	Offset      int64  `json:"offset,omitempty"`
	Length      int64  `json:"length"`
	Total       int64  `json:"total,omitempty"`
	BodySHA256  string `json:"body_sha256,omitempty"`
	BlobHash    string `json:"blob_hash,omitempty"`
	ContentType string `json:"content_type,omitempty"`
}

func (s *Server) resolveArtifactSource(source *artifactSource, secret bool) ([]byte, artifactProvenance, error) {
	if source == nil {
		return nil, artifactProvenance{}, nil
	}
	kinds := 0
	if source.Literal != nil {
		kinds++
	}
	if source.Base64 != nil {
		kinds++
	}
	if strings.TrimSpace(source.TrafficID) != "" {
		kinds++
	}
	if kinds != 1 {
		return nil, artifactProvenance{}, errors.New("source must specify exactly one of literal, base64, or traffic_id")
	}
	limit := artifacttransform.MaxInputBytes
	if secret {
		limit = artifacttransform.MaxSecretBytes
	}
	if source.Literal != nil {
		data := []byte(*source.Literal)
		if len(data) > limit {
			return nil, artifactProvenance{}, fmt.Errorf("literal exceeds %d bytes", limit)
		}
		return data, artifactProvenance{Kind: "literal", Length: int64(len(data))}, nil
	}
	if source.Base64 != nil {
		data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(*source.Base64))
		if err != nil {
			return nil, artifactProvenance{}, errors.New("source base64 is invalid")
		}
		if len(data) > limit {
			return nil, artifactProvenance{}, fmt.Errorf("decoded base64 exceeds %d bytes", limit)
		}
		return data, artifactProvenance{Kind: "base64", Length: int64(len(data))}, nil
	}
	if s.m == nil || s.m.Traffic() == nil {
		return nil, artifactProvenance{}, errors.New("captured traffic is unavailable")
	}
	side := strings.ToLower(strings.TrimSpace(source.Side))
	if side == "" {
		side = "response"
	}
	length := source.Length
	if length <= 0 {
		length = int64(limit)
	}
	if length > int64(limit) {
		return nil, artifactProvenance{}, fmt.Errorf("traffic range exceeds %d bytes", limit)
	}
	body, err := s.m.Traffic().ReadBodyRange(source.TrafficID, side, source.Offset, length)
	if err != nil {
		return nil, artifactProvenance{}, err
	}
	return body.Data, artifactProvenance{
		Kind: "captured_traffic", TrafficID: strings.TrimSpace(source.TrafficID), Side: side,
		Offset: body.Offset, Length: body.Length, Total: body.Total, BodySHA256: body.SHA256,
		BlobHash: body.BlobHash, ContentType: body.ContentType,
	}, nil
}

func artifactSourceSchema(description string) map[string]any {
	return map[string]any{
		"type": "object", "description": description,
		"properties": map[string]any{
			"literal":    map[string]any{"type": "string", "description": "literal UTF-8 bytes"},
			"base64":     map[string]any{"type": "string", "description": "standard-base64 encoded binary bytes"},
			"traffic_id": map[string]any{"type": "string", "description": "captured exchange id from http_request/traffic_search"},
			"side":       map[string]any{"type": "string", "enum": []string{"request", "response"}, "description": "captured body side, default response"},
			"offset":     map[string]any{"type": "integer", "description": "captured body byte offset, default 0"},
			"length":     map[string]any{"type": "integer", "description": "captured body byte length; max 1 MiB for input and 64 KiB for secret"},
		},
	}
}

func (s *Server) artifactTransformTool() actool.CoreTool {
	schema := objSchema(map[string]any{
		"input":  artifactSourceSchema("input bytes; exactly one source field is allowed"),
		"secret": artifactSourceSchema("optional secret/key bytes; its literal/base64 value is redacted from tool audit surfaces"),
		"operations": map[string]any{
			"type": "array", "maxItems": artifacttransform.MaxOperations,
			"items": map[string]any{"type": "object", "properties": map[string]any{
				"name":      map[string]any{"type": "string", "enum": []string{"base64_encode", "base64_decode", "base64url_encode", "base64url_decode", "hex_encode", "hex_decode", "url_encode", "url_decode", "gzip_compress", "gzip_decompress", "zlib_compress", "zlib_decompress", "hash", "hmac"}},
				"algorithm": map[string]any{"type": "string", "description": "sha1, sha256, or sha512 for hash/HMAC"},
			}, "required": []any{"name"}},
		},
		"recipe":          map[string]any{"type": "string", "enum": []string{"jwt_inspect", "jwt_hs_sign", "flask_session_decode", "flask_session_sign"}, "description": "named bounded recipe; mutually exclusive with operations"},
		"algorithm":       map[string]any{"type": "string", "enum": []string{"HS256", "HS384", "HS512"}, "description": "JWT HMAC algorithm"},
		"payload_json":    map[string]any{"type": "object", "description": "JSON object used by signing recipes"},
		"issued_at_unix":  map[string]any{"type": "integer", "description": "optional deterministic Flask session timestamp; defaults to current Unix time"},
		"output_encoding": map[string]any{"type": "string", "enum": []string{"auto", "utf8", "base64", "base64url", "hex"}, "description": "rendering for the bounded result, default auto"},
	})
	return actool.Build(actool.Spec{
		Name:        "artifact_transform",
		Description: "Apply a bounded deterministic encoding, compression, hash/HMAC, JWT-HS, or Flask/itsdangerous session operation to literal bytes or an immutable captured traffic range. This tool has no filesystem, network, process, brute-force, package-install, or arbitrary-code capability. Use it to construct and inspect target-derived protocol/session artifacts; secrets are accepted only as bounded inputs and redacted from activity audit arguments.",
		Schema:      schema,
		ReadOnly:    func(json.RawMessage) bool { return true },
		Concurrent:  func(json.RawMessage) bool { return true },
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(ctx context.Context, in json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
			var args artifactTransformArgs
			if err := json.Unmarshal(in, &args); err != nil {
				return actool.Errorf("invalid artifact_transform input: " + err.Error()), nil
			}
			input, inputSource, err := s.resolveArtifactSource(args.Input, false)
			if err != nil {
				return actool.Errorf("input: " + err.Error()), nil
			}
			secret, secretSource, err := s.resolveArtifactSource(args.Secret, true)
			if err != nil {
				return actool.Errorf("secret: " + err.Error()), nil
			}
			timeoutCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			result, err := artifacttransform.Execute(timeoutCtx, input, secret, artifacttransform.Request{
				Operations: args.Operations, Recipe: args.Recipe, Algorithm: args.Algorithm,
				PayloadJSON: args.PayloadJSON, IssuedAtUnix: args.IssuedAtUnix, OutputEncoding: args.OutputEncoding,
			})
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			output := map[string]any{"result": result}
			if args.Input != nil {
				output["input_source"] = inputSource
			}
			if args.Secret != nil {
				// Deliberately expose only provenance/length, never the secret value
				// or a reusable digest of it.
				secretSource.BodySHA256 = ""
				secretSource.BlobHash = ""
				output["secret_source"] = secretSource
			}
			encoded, err := json.Marshal(output)
			if err != nil {
				return actool.Errorf("encode artifact_transform result: " + err.Error()), nil
			}
			return actool.Text(actool.Capture(tc, string(encoded))), nil
		},
	})
}
