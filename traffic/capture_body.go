package traffic

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// A reader borrows the existing capture store. It does not coordinate writers,
// retain bodies between calls, or change the recorder/evidence authority.
type captureReader struct{ store *Traffic }

func (reader captureReader) blobPath(raw string) (string, error) {
	hash := strings.ToLower(strings.TrimSpace(raw))
	if !blobHashRe.MatchString(hash) {
		return "", fmt.Errorf("invalid blob hash")
	}
	base := filepath.Join(reader.store.dir, "_blobs", "sha256", hash[:2])
	for _, candidate := range []string{filepath.Join(base, hash+".bin"), filepath.Join(base, hash[2:4], hash+".bin")} {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("blob %s does not exist", hash)
}
func (reader captureReader) openBlob(hash string) (*os.File, int64, error) {
	location, err := reader.blobPath(hash)
	if err != nil {
		return nil, 0, err
	}
	file, err := os.Open(location)
	if err != nil {
		return nil, 0, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, 0, err
	}
	return file, info.Size(), nil
}
func (reader captureReader) blobRange(hash string, offset, length int64) ([]byte, int64, error) {
	file, total, err := reader.openBlob(hash)
	if err != nil {
		return nil, 0, err
	}
	defer file.Close()
	if offset < 0 {
		offset = 0
	}
	if offset >= total {
		return nil, total, nil
	}
	if length <= 0 || offset+length > total {
		length = total - offset
	}
	data := make([]byte, length)
	read, err := file.ReadAt(data, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, total, err
	}
	return data[:read], total, nil
}

type capturedBodySide struct {
	head   string
	data   []byte
	blob   sql.NullString
	length int64
}
type capturedBodyRecord struct {
	request, response  capturedBodySide
	responseType, path string
}

func (reader captureReader) indexBody(id string) (capturedBodyRecord, error) {
	var record capturedBodyRecord
	err := reader.store.db.QueryRow(`SELECT b.req_head,b.req_body,b.req_blob,b.resp_head,b.resp_body,b.resp_blob,
e.req_len,e.resp_len,e.content_type,e.path
FROM exchange_bodies b JOIN exchanges e ON e.id=b.id WHERE b.id=?`, id).Scan(
		&record.request.head, &record.request.data, &record.request.blob,
		&record.response.head, &record.response.data, &record.response.blob,
		&record.request.length, &record.response.length, &record.responseType, &record.path)
	return record, err
}

type captureHashPolicy uint8

const (
	hashBoundedInline captureHashPolicy = iota
	hashFullLegacy
)

func capturedPreview(data []byte, total int64, mediaType, source string, policy captureHashPolicy) inspectBody {
	var digest [sha256.Size]byte
	if policy == hashFullLegacy {
		digest = sha256.Sum256(data)
	}
	bounded := len(data) > maxInspectBody
	complete := !bounded && int64(len(data)) >= total
	if bounded {
		data = data[:maxInspectBody]
	}
	if policy == hashBoundedInline {
		digest = sha256.Sum256(data)
	}
	return inspectBody{Data: data, Total: total, SHA256: hex.EncodeToString(digest[:]), ContentType: mediaType, Source: source, Complete: complete}
}
func (reader captureReader) inspectBlob(reference, mediaType string) (inspectBody, error) {
	file, total, err := reader.openBlob(reference)
	if err != nil {
		return inspectBody{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxInspectBody+1))
	if err != nil {
		return inspectBody{}, err
	}
	complete := int64(len(data)) <= maxInspectBody
	if !complete {
		data = data[:maxInspectBody]
	}
	// File lookup normalizes a hash; evidence preserves its stored spelling.
	return inspectBody{Data: data, Total: total, SHA256: reference, BlobHash: reference, ContentType: mediaType, Source: "blob", Complete: complete}, nil
}
func (reader captureReader) inspectIndexed(record capturedBodyRecord, side string) (inspectBody, error) {
	selected, mediaType := record.response, record.responseType
	if side == "request" {
		selected = record.request
		mediaType = headerValue(selected.head, "Content-Type")
	}
	if selected.blob.Valid && selected.blob.String != "" {
		return reader.inspectBlob(selected.blob.String, mediaType)
	}
	if selected.length <= 0 {
		selected.length = int64(len(selected.data))
	}
	return capturedPreview(selected.data, selected.length, mediaType, "inline", hashBoundedInline), nil
}
func (reader captureReader) inspectLegacy(id, side string) (inspectBody, error) {
	var relative, mediaType string
	if err := reader.store.db.QueryRow(`SELECT path,content_type FROM exchanges WHERE id=?`, id).Scan(&relative, &mediaType); err != nil {
		return inspectBody{}, err
	}
	if strings.TrimSpace(relative) == "" {
		return inspectBody{}, fmt.Errorf("exchange %s has no body record", id)
	}
	filename := "response.http"
	if side == "request" {
		filename = "request.http"
		mediaType = ""
	}
	raw, err := os.ReadFile(filepath.Join(reader.store.dir, relative, filename))
	if err != nil {
		return inspectBody{}, err
	}
	head, data := splitRawHTTP(raw)
	if side == "request" {
		mediaType = headerValue(string(head), "Content-Type")
	}
	return capturedPreview(data, int64(len(data)), mediaType, "legacy", hashFullLegacy), nil
}
func (reader captureReader) inspect(id, side string) (inspectBody, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return inspectBody{}, errors.New("exchange id is required")
	}
	side = strings.ToLower(strings.TrimSpace(side))
	if side != "request" && side != "response" {
		return inspectBody{}, fmt.Errorf("side must be request or response")
	}
	record, err := reader.indexBody(id)
	if err == nil {
		return reader.inspectIndexed(record, side)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return inspectBody{}, err
	}
	return reader.inspectLegacy(id, side)
}

func (reader captureReader) bodyRange(id, side string, offset, length int64) (BodyRange, error) {
	if offset < 0 {
		return BodyRange{}, errors.New("offset must be non-negative")
	}
	if length <= 0 || length > 1<<20 {
		return BodyRange{}, errors.New("length must be between 1 and 1048576 bytes")
	}
	body, err := reader.inspect(id, side)
	if err != nil {
		return BodyRange{}, err
	}
	result := BodyRange{Offset: offset, Total: body.Total, SHA256: body.SHA256, ContentType: body.ContentType, Source: body.Source}
	if body.BlobHash != "" {
		data, total, err := reader.blobRange(body.BlobHash, offset, length)
		if err != nil {
			return BodyRange{}, err
		}
		result.Data, result.Total, result.Length, result.BlobHash = data, total, int64(len(data)), body.BlobHash
		return result, nil
	}
	if offset >= int64(len(body.Data)) {
		if !body.Complete && offset < body.Total {
			if body.Source == "inline" && len(body.Data) < maxInspectBody {
				return BodyRange{}, errors.New("requested offset is beyond the incomplete captured body preview")
			}
			return BodyRange{}, errors.New("requested offset is beyond the bounded legacy capture preview")
		}
		return result, nil
	}
	end := offset + length
	if end > int64(len(body.Data)) {
		end = int64(len(body.Data))
	}
	result.Data = append([]byte(nil), body.Data[offset:end]...)
	result.Length = int64(len(result.Data))
	return result, nil
}
