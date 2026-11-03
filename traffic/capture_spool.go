package traffic

import (
	"crypto/sha256"
	"encoding/hex"
	"log"
	"os"
	"path/filepath"
)

type captureBodyWrite struct {
	directory, contentType string
	bytes                  []byte
}

func (body captureBodyWrite) materialize() storedBody {
	if len(body.bytes) == 0 {
		return storedBody{}
	}
	text := !isBinaryBody(body.contentType, body.bytes)
	if len(body.bytes) <= maxInlineBody {
		return storedBody{inline: body.bytes, index: body.index(text)}
	}
	hash, stored := body.blob()
	if !stored {
		return storedBody{inline: clipBytes(body.bytes, blobPreview), index: body.index(text)}
	}
	result := storedBody{hash: hash, index: body.index(text)}
	if text {
		result.inline = []byte(truncateUTF8(body.bytes, blobPreview))
	} else {
		result.inline = []byte(binaryTag(body.contentType, body.bytes))
	}
	return result
}

func (body captureBodyWrite) index(text bool) string {
	if !text {
		return ""
	}
	return string(clipBytes(body.bytes, maxIndexBody))
}

func (body captureBodyWrite) blob() (string, bool) {
	digest := sha256.Sum256(body.bytes)
	hash := hex.EncodeToString(digest[:])
	directory := filepath.Join(body.directory, "_blobs", "sha256", hash[:2])
	if err := os.MkdirAll(directory, 0755); err != nil {
		log.Printf("[traffic] failed to create blob directory: %v", err)
		return "", false
	}
	file := filepath.Join(directory, hash+".bin")
	if _, err := os.Stat(file); os.IsNotExist(err) {
		if err := os.WriteFile(file, body.bytes, 0644); err != nil {
			log.Printf("[traffic] failed to write blob %s: %v", hash, err)
			return "", false
		}
	}
	return hash, true
}
