// Package binarybroker owns the private content-addressed store used by the
// binary-artifact boundary. Callers provide byte streams and opaque IDs, never
// filesystem paths. The store is intended to live on a volume that is not
// mounted into the Uroboros application or analyzer workers.
package binarybroker

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	StorageVersion    = 1
	MaxArtifactBytes  = int64(512 << 20)
	MinimumGCGrace    = 24 * time.Hour
	DefaultReceiptTTL = 48 * time.Hour
	maxLedgerBytes    = int64(64 << 10)
)

var (
	ErrInvalidID         = errors.New("invalid broker identifier")
	ErrInvalidSize       = errors.New("invalid artifact size")
	ErrArtifactTooLarge  = errors.New("artifact exceeds broker size ceiling")
	ErrReceiptNotFound   = errors.New("broker receipt not found")
	ErrReferenceConflict = errors.New("broker reference conflicts with existing state")
	ErrCorruptStore      = errors.New("binary broker store is corrupt")
	ErrGraceTooShort     = errors.New("binary broker GC grace is below the safety floor")
)

var (
	idPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,191}$`)
	hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type Config struct {
	Root       string
	ReceiptTTL time.Duration
	Now        func() time.Time
}

// Receipt is the durable hand-off between a completed stream and a database
// reference. A receipt roots its blob until it is committed or expires.
type Receipt struct {
	ID             string    `json:"id"`
	SHA256         string    `json:"sha256"`
	ByteSize       int64     `json:"byte_size"`
	StorageVersion int       `json:"storage_version"`
	CreatedAt      time.Time `json:"created_at"`
	ExpiresAt      time.Time `json:"expires_at"`
}

// Reference is an opaque durable root. Its ID is hashed before it is used as a
// ledger filename, so even a valid identifier never becomes a caller path.
type Reference struct {
	ID             string    `json:"id"`
	ReceiptID      string    `json:"receipt_id,omitempty"`
	SHA256         string    `json:"sha256"`
	ByteSize       int64     `json:"byte_size"`
	StorageVersion int       `json:"storage_version"`
	CreatedAt      time.Time `json:"created_at"`
}

type Inventory struct {
	References []Reference `json:"references"`
	Receipts   []Receipt   `json:"receipts"`
}

type ReconcileReport struct {
	ExpiredReceipts int `json:"expired_receipts"`
	StaleTemps      int `json:"stale_temps"`
	MarkersCreated  int `json:"markers_created"`
	BlobsDeleted    int `json:"blobs_deleted"`
}

type unreferencedMarker struct {
	SHA256       string    `json:"sha256"`
	Unreferenced time.Time `json:"unreferenced_at"`
}

type Store struct {
	root       string
	receiptTTL time.Duration
	now        func() time.Time
	mu         sync.Mutex
}

func NewStore(config Config) (*Store, error) {
	root := filepath.Clean(strings.TrimSpace(config.Root))
	if root == "." || root == "" || !filepath.IsAbs(root) {
		return nil, fmt.Errorf("broker root must be an absolute path")
	}
	ttl := config.ReceiptTTL
	if ttl == 0 {
		ttl = DefaultReceiptTTL
	}
	if ttl < MinimumGCGrace {
		return nil, fmt.Errorf("receipt TTL: %w", ErrGraceTooShort)
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	if err := ensurePrivateDirectory(root); err != nil {
		return nil, err
	}
	for _, relative := range []string{"blobs/sha256", "receipts", "refs", "staging", "unreferenced"} {
		if err := ensurePrivateDirectory(filepath.Join(root, filepath.FromSlash(relative))); err != nil {
			return nil, err
		}
	}
	return &Store{root: root, receiptTTL: ttl, now: now}, nil
}

func ensurePrivateDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%w: broker directory is not a real directory", ErrCorruptStore)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	if !samePath(resolved, path) {
		return fmt.Errorf("%w: broker directory traverses a symlink", ErrCorruptStore)
	}
	return os.Chmod(path, 0o700)
}

func samePath(left, right string) bool {
	left, right = filepath.Clean(left), filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func validID(id string) bool { return idPattern.MatchString(id) }

func validateHash(hash string) error {
	if !hashPattern.MatchString(hash) {
		return fmt.Errorf("%w: invalid sha256", ErrCorruptStore)
	}
	return nil
}

func randomID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "receipt-" + hex.EncodeToString(raw[:]), nil
}

func ledgerName(id string) string {
	hash := sha256.Sum256([]byte(id))
	return hex.EncodeToString(hash[:]) + ".json"
}

func (store *Store) receiptPath(id string) string {
	return filepath.Join(store.root, "receipts", ledgerName(id))
}

func (store *Store) referencePath(id string) string {
	return filepath.Join(store.root, "refs", ledgerName(id))
}

func (store *Store) markerPath(hash string) string {
	return filepath.Join(store.root, "unreferenced", hash+".json")
}

// openReference is intentionally package-private: only the broker's worker
// coordinator may turn an opaque reference into a byte stream. Neither the
// application protocol nor analyzer protocol ever receives a CAS path.
func (store *Store) openReference(referenceID string) (Reference, *os.File, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if !validID(referenceID) {
		return Reference{}, nil, ErrInvalidID
	}
	var reference Reference
	if err := readJSONFile(store.referencePath(referenceID), &reference); err != nil {
		return Reference{}, nil, err
	}
	if reference.ID != referenceID || !validReference(reference) {
		return Reference{}, nil, fmt.Errorf("%w: reference identity mismatch", ErrCorruptStore)
	}
	path, err := store.blobPath(reference.SHA256)
	if err != nil {
		return Reference{}, nil, err
	}
	if err := validateBlob(path, reference.SHA256, reference.ByteSize); err != nil {
		return Reference{}, nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return Reference{}, nil, err
	}
	return reference, file, nil
}

func (store *Store) blobPath(hash string) (string, error) {
	if err := validateHash(hash); err != nil {
		return "", err
	}
	dir := filepath.Join(store.root, "blobs", "sha256", hash[:2])
	if err := ensurePrivateDirectory(dir); err != nil {
		return "", err
	}
	return filepath.Join(dir, hash+".bin"), nil
}

// Stage streams and hashes one artifact, stores it in CAS, and persists a
// receipt before returning. The reader is never interpreted as a path.
func (store *Store) Stage(reader io.Reader, expectedSize int64) (Receipt, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if reader == nil || expectedSize < 0 {
		return Receipt{}, ErrInvalidSize
	}
	if expectedSize > MaxArtifactBytes {
		return Receipt{}, ErrArtifactTooLarge
	}
	temp, err := os.CreateTemp(filepath.Join(store.root, "staging"), "artifact-*.tmp")
	if err != nil {
		return Receipt{}, err
	}
	tempPath := temp.Name()
	removeTemp := true
	defer func() {
		_ = temp.Close()
		if removeTemp {
			_ = os.Remove(tempPath)
		}
	}()
	if err := temp.Chmod(0o600); err != nil {
		return Receipt{}, err
	}
	hasher := sha256.New()
	readLimit := expectedSize + 1
	if readLimit > MaxArtifactBytes+1 {
		readLimit = MaxArtifactBytes + 1
	}
	written, err := io.Copy(io.MultiWriter(temp, hasher), io.LimitReader(reader, readLimit))
	if err != nil {
		return Receipt{}, err
	}
	if written > MaxArtifactBytes {
		return Receipt{}, ErrArtifactTooLarge
	}
	if written != expectedSize {
		return Receipt{}, fmt.Errorf("%w: expected %d bytes, received %d", ErrInvalidSize, expectedSize, written)
	}
	if err := temp.Sync(); err != nil {
		return Receipt{}, err
	}
	if err := temp.Close(); err != nil {
		return Receipt{}, err
	}
	hash := hex.EncodeToString(hasher.Sum(nil))
	blobPath, err := store.blobPath(hash)
	if err != nil {
		return Receipt{}, err
	}
	if err := os.Link(tempPath, blobPath); err != nil {
		if !os.IsExist(err) {
			return Receipt{}, fmt.Errorf("install CAS blob: %w", err)
		}
		if err := validateBlob(blobPath, hash, written); err != nil {
			return Receipt{}, err
		}
	} else if err := syncDirectory(filepath.Dir(blobPath)); err != nil {
		return Receipt{}, err
	}
	if err := os.Remove(tempPath); err != nil {
		return Receipt{}, err
	}
	removeTemp = false
	id, err := randomID()
	if err != nil {
		return Receipt{}, err
	}
	now := store.now().UTC()
	receipt := Receipt{
		ID: id, SHA256: hash, ByteSize: written, StorageVersion: StorageVersion,
		CreatedAt: now, ExpiresAt: now.Add(store.receiptTTL),
	}
	if err := writeJSONExclusive(store.receiptPath(id), receipt); err != nil {
		return Receipt{}, err
	}
	if err := store.clearUnreferencedMarker(hash); err != nil {
		return Receipt{}, err
	}
	return receipt, nil
}

// Commit converts a staged receipt into a durable reference. It is idempotent
// when the reference already exists with the same content.
func (store *Store) Commit(receiptID, referenceID string) (Reference, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if !validID(receiptID) || !validID(referenceID) {
		return Reference{}, ErrInvalidID
	}
	var receipt Receipt
	err := readJSONFile(store.receiptPath(receiptID), &receipt)
	if os.IsNotExist(err) {
		var existing Reference
		if refErr := readJSONFile(store.referencePath(referenceID), &existing); refErr == nil {
			if existing.ID == referenceID && existing.ReceiptID == receiptID && validReference(existing) {
				blobPath, blobErr := store.blobPath(existing.SHA256)
				if blobErr != nil {
					return Reference{}, blobErr
				}
				if blobErr = validateBlob(blobPath, existing.SHA256, existing.ByteSize); blobErr != nil {
					return Reference{}, blobErr
				}
				if blobErr = store.clearUnreferencedMarker(existing.SHA256); blobErr != nil {
					return Reference{}, blobErr
				}
				return existing, nil
			}
			return Reference{}, ErrReferenceConflict
		}
		return Reference{}, ErrReceiptNotFound
	}
	if err != nil {
		return Reference{}, err
	}
	if receipt.ID != receiptID || !validReceipt(receipt) {
		return Reference{}, fmt.Errorf("%w: invalid receipt", ErrCorruptStore)
	}
	blobPath, err := store.blobPath(receipt.SHA256)
	if err != nil {
		return Reference{}, err
	}
	if err := validateBlob(blobPath, receipt.SHA256, receipt.ByteSize); err != nil {
		return Reference{}, err
	}
	reference := Reference{
		ID: referenceID, ReceiptID: receiptID, SHA256: receipt.SHA256, ByteSize: receipt.ByteSize,
		StorageVersion: receipt.StorageVersion, CreatedAt: store.now().UTC(),
	}
	if err := store.putReference(reference); err != nil {
		return Reference{}, err
	}
	if err := os.Remove(store.receiptPath(receiptID)); err != nil && !os.IsNotExist(err) {
		return Reference{}, err
	}
	if err := store.clearUnreferencedMarker(receipt.SHA256); err != nil {
		return Reference{}, err
	}
	if err := syncDirectory(filepath.Join(store.root, "receipts")); err != nil {
		return Reference{}, err
	}
	return reference, nil
}

// Bind clones an existing durable reference without exposing a hash-selected
// read primitive. Archive transitions use this to add the destination reference
// before removing the source reference.
func (store *Store) Bind(sourceID, destinationID string) (Reference, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if !validID(sourceID) || !validID(destinationID) {
		return Reference{}, ErrInvalidID
	}
	var source Reference
	if err := readJSONFile(store.referencePath(sourceID), &source); err != nil {
		return Reference{}, err
	}
	if source.ID != sourceID || !validReference(source) {
		return Reference{}, fmt.Errorf("%w: invalid source reference", ErrCorruptStore)
	}
	blobPath, err := store.blobPath(source.SHA256)
	if err != nil {
		return Reference{}, err
	}
	if err := validateBlob(blobPath, source.SHA256, source.ByteSize); err != nil {
		return Reference{}, err
	}
	destination := Reference{
		ID: destinationID, SHA256: source.SHA256, ByteSize: source.ByteSize,
		StorageVersion: source.StorageVersion, CreatedAt: store.now().UTC(),
	}
	if err := store.putReference(destination); err != nil {
		return Reference{}, err
	}
	if err := store.clearUnreferencedMarker(source.SHA256); err != nil {
		return Reference{}, err
	}
	return destination, nil
}

func (store *Store) putReference(reference Reference) error {
	path := store.referencePath(reference.ID)
	var existing Reference
	if err := readJSONFile(path, &existing); err == nil {
		if validReference(existing) && existing.ID == reference.ID && existing.SHA256 == reference.SHA256 && existing.ByteSize == reference.ByteSize && existing.StorageVersion == reference.StorageVersion {
			return nil
		}
		return ErrReferenceConflict
	} else if !os.IsNotExist(err) {
		return err
	}
	return writeJSONExclusive(path, reference)
}

func validReference(reference Reference) bool {
	return validID(reference.ID) &&
		(reference.ReceiptID == "" || validID(reference.ReceiptID)) &&
		validateHash(reference.SHA256) == nil &&
		reference.ByteSize >= 0 && reference.ByteSize <= MaxArtifactBytes &&
		reference.StorageVersion == StorageVersion && !reference.CreatedAt.IsZero()
}

func validReceipt(receipt Receipt) bool {
	return validID(receipt.ID) && validateHash(receipt.SHA256) == nil &&
		receipt.ByteSize >= 0 && receipt.ByteSize <= MaxArtifactBytes &&
		receipt.StorageVersion == StorageVersion && !receipt.CreatedAt.IsZero() &&
		!receipt.ExpiresAt.IsZero() && !receipt.ExpiresAt.Before(receipt.CreatedAt)
}

// Release removes one durable reference. The blob is only marked as
// unreferenced here; Reconcile enforces the minimum grace before deletion.
func (store *Store) Release(referenceID string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if !validID(referenceID) {
		return ErrInvalidID
	}
	var reference Reference
	if err := readJSONFile(store.referencePath(referenceID), &reference); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if reference.ID != referenceID || !validReference(reference) {
		return fmt.Errorf("%w: reference identity mismatch", ErrCorruptStore)
	}
	if err := os.Remove(store.referencePath(referenceID)); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := syncDirectory(filepath.Join(store.root, "refs")); err != nil {
		return err
	}
	return store.markUnreferencedIfNeeded(reference.SHA256, store.now().UTC())
}

func (store *Store) DiscardReceipt(receiptID string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if !validID(receiptID) {
		return ErrInvalidID
	}
	var receipt Receipt
	if err := readJSONFile(store.receiptPath(receiptID), &receipt); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if receipt.ID != receiptID || !validReceipt(receipt) {
		return fmt.Errorf("%w: receipt identity mismatch", ErrCorruptStore)
	}
	if err := os.Remove(store.receiptPath(receiptID)); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := syncDirectory(filepath.Join(store.root, "receipts")); err != nil {
		return err
	}
	return store.markUnreferencedIfNeeded(receipt.SHA256, store.now().UTC())
}

func (store *Store) Inventory() (Inventory, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.inventoryUnlocked()
}

func (store *Store) inventoryUnlocked() (Inventory, error) {
	var inventory Inventory
	verifiedBlobs := make(map[string]struct{})
	verifyBlob := func(hash string, size int64) error {
		if _, ok := verifiedBlobs[hash]; ok {
			return nil
		}
		path, err := store.blobPath(hash)
		if err != nil {
			return err
		}
		if err := validateBlob(path, hash, size); err != nil {
			return err
		}
		verifiedBlobs[hash] = struct{}{}
		return nil
	}
	if err := readLedgerDirectory(filepath.Join(store.root, "refs"), func(path string) error {
		var reference Reference
		if err := readJSONFile(path, &reference); err != nil {
			return err
		}
		if !validReference(reference) || filepath.Base(path) != ledgerName(reference.ID) {
			return fmt.Errorf("%w: invalid reference ledger", ErrCorruptStore)
		}
		if err := verifyBlob(reference.SHA256, reference.ByteSize); err != nil {
			return err
		}
		inventory.References = append(inventory.References, reference)
		return nil
	}); err != nil {
		return Inventory{}, err
	}
	if err := readLedgerDirectory(filepath.Join(store.root, "receipts"), func(path string) error {
		var receipt Receipt
		if err := readJSONFile(path, &receipt); err != nil {
			return err
		}
		if !validReceipt(receipt) || filepath.Base(path) != ledgerName(receipt.ID) {
			return fmt.Errorf("%w: invalid receipt ledger", ErrCorruptStore)
		}
		if err := verifyBlob(receipt.SHA256, receipt.ByteSize); err != nil {
			return err
		}
		inventory.Receipts = append(inventory.Receipts, receipt)
		return nil
	}); err != nil {
		return Inventory{}, err
	}
	sort.Slice(inventory.References, func(i, j int) bool { return inventory.References[i].ID < inventory.References[j].ID })
	sort.Slice(inventory.Receipts, func(i, j int) bool { return inventory.Receipts[i].ID < inventory.Receipts[j].ID })
	return inventory, nil
}

// Reconcile expires abandoned receipts, cleans stale private temp files, and
// garbage-collects unreferenced blobs only after the enforced grace period.
func (store *Store) Reconcile(now time.Time, grace time.Duration) (ReconcileReport, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	var report ReconcileReport
	if grace < MinimumGCGrace {
		return report, ErrGraceTooShort
	}
	now = now.UTC()
	inventory, err := store.inventoryUnlocked()
	if err != nil {
		return report, err
	}
	for _, receipt := range inventory.Receipts {
		if receipt.ExpiresAt.After(now) {
			continue
		}
		if err := os.Remove(store.receiptPath(receipt.ID)); err != nil && !os.IsNotExist(err) {
			return report, err
		}
		report.ExpiredReceipts++
	}
	if report.ExpiredReceipts > 0 {
		if err := syncDirectory(filepath.Join(store.root, "receipts")); err != nil {
			return report, err
		}
	}
	entries, err := os.ReadDir(filepath.Join(store.root, "staging"))
	if err != nil {
		return report, err
	}
	for _, entry := range entries {
		path := filepath.Join(store.root, "staging", entry.Name())
		info, err := entry.Info()
		if err != nil {
			return report, err
		}
		if !info.Mode().IsRegular() {
			return report, fmt.Errorf("%w: non-regular staging entry", ErrCorruptStore)
		}
		if !info.ModTime().Add(store.receiptTTL).After(now) {
			if err := os.Remove(path); err != nil {
				return report, err
			}
			report.StaleTemps++
		}
	}
	for _, ledgerDir := range []string{"refs", "receipts"} {
		removed, err := cleanupStaleLedgerTemps(filepath.Join(store.root, ledgerDir), now, store.receiptTTL)
		if err != nil {
			return report, err
		}
		report.StaleTemps += removed
	}
	inventory, err = store.inventoryUnlocked()
	if err != nil {
		return report, err
	}
	rooted := make(map[string]struct{}, len(inventory.References)+len(inventory.Receipts))
	for _, reference := range inventory.References {
		rooted[reference.SHA256] = struct{}{}
	}
	for _, receipt := range inventory.Receipts {
		rooted[receipt.SHA256] = struct{}{}
	}
	blobs, err := store.listBlobs()
	if err != nil {
		return report, err
	}
	for hash, path := range blobs {
		if _, ok := rooted[hash]; ok {
			if err := store.clearUnreferencedMarker(hash); err != nil {
				return report, err
			}
			continue
		}
		var marker unreferencedMarker
		err := readJSONFile(store.markerPath(hash), &marker)
		if os.IsNotExist(err) {
			marker = unreferencedMarker{SHA256: hash, Unreferenced: now}
			if err := writeJSONExclusive(store.markerPath(hash), marker); err != nil {
				return report, err
			}
			report.MarkersCreated++
			continue
		}
		if err != nil || marker.SHA256 != hash || marker.Unreferenced.IsZero() {
			return report, fmt.Errorf("%w: invalid unreferenced marker", ErrCorruptStore)
		}
		if marker.Unreferenced.Add(grace).After(now) {
			continue
		}
		if err := os.Chmod(path, 0o600); err != nil {
			return report, err
		}
		if err := os.Remove(path); err != nil {
			return report, err
		}
		if err := syncDirectory(filepath.Dir(path)); err != nil {
			return report, err
		}
		if err := os.Remove(store.markerPath(hash)); err != nil && !os.IsNotExist(err) {
			return report, err
		}
		if err := syncDirectory(filepath.Join(store.root, "unreferenced")); err != nil {
			return report, err
		}
		report.BlobsDeleted++
	}
	return report, nil
}

func (store *Store) markUnreferencedIfNeeded(hash string, now time.Time) error {
	inventory, err := store.inventoryUnlocked()
	if err != nil {
		return err
	}
	for _, reference := range inventory.References {
		if reference.SHA256 == hash {
			return nil
		}
	}
	for _, receipt := range inventory.Receipts {
		if receipt.SHA256 == hash {
			return nil
		}
	}
	path := store.markerPath(hash)
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("%w: invalid unreferenced marker", ErrCorruptStore)
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		if err := syncDirectory(filepath.Dir(path)); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return writeJSONExclusive(path, unreferencedMarker{SHA256: hash, Unreferenced: now})
}

func (store *Store) clearUnreferencedMarker(hash string) error {
	path := store.markerPath(hash)
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("%w: invalid unreferenced marker", ErrCorruptStore)
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func (store *Store) listBlobs() (map[string]string, error) {
	root := filepath.Join(store.root, "blobs", "sha256")
	prefixes, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	blobs := make(map[string]string)
	for _, prefix := range prefixes {
		if !prefix.IsDir() || len(prefix.Name()) != 2 || strings.ToLower(prefix.Name()) != prefix.Name() {
			return nil, fmt.Errorf("%w: invalid CAS prefix", ErrCorruptStore)
		}
		dir := filepath.Join(root, prefix.Name())
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			name := entry.Name()
			if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() || !strings.HasSuffix(name, ".bin") {
				return nil, fmt.Errorf("%w: non-regular CAS entry", ErrCorruptStore)
			}
			hash := strings.TrimSuffix(name, ".bin")
			if validateHash(hash) != nil || hash[:2] != prefix.Name() {
				return nil, fmt.Errorf("%w: invalid CAS filename", ErrCorruptStore)
			}
			blobs[hash] = filepath.Join(dir, name)
		}
	}
	return blobs, nil
}

func readLedgerDirectory(dir string, visit func(string) error) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if isLedgerTempName(entry.Name()) {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > maxLedgerBytes {
				return fmt.Errorf("%w: invalid temporary ledger entry", ErrCorruptStore)
			}
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".json") {
			return fmt.Errorf("%w: invalid ledger entry", ErrCorruptStore)
		}
		if err := visit(filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func isLedgerTempName(name string) bool {
	return strings.HasPrefix(name, ".ledger-") && strings.HasSuffix(name, ".tmp") && len(name) > len(".ledger-.tmp")
}

func cleanupStaleLedgerTemps(dir string, now time.Time, maxAge time.Duration) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, entry := range entries {
		if !isLedgerTempName(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return removed, err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > maxLedgerBytes {
			return removed, fmt.Errorf("%w: invalid temporary ledger entry", ErrCorruptStore)
		}
		if info.ModTime().Add(maxAge).After(now) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
			return removed, err
		}
		removed++
	}
	if removed > 0 {
		if err := syncDirectory(dir); err != nil {
			return removed, err
		}
	}
	return removed, nil
}

func validateBlob(path, wantHash string, wantSize int64) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() != wantSize {
		return fmt.Errorf("%w: CAS blob metadata mismatch", ErrCorruptStore)
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	hasher := sha256.New()
	written, err := io.Copy(hasher, io.LimitReader(file, MaxArtifactBytes+1))
	if err != nil {
		return err
	}
	if written != wantSize || hex.EncodeToString(hasher.Sum(nil)) != wantHash {
		return fmt.Errorf("%w: CAS blob hash mismatch", ErrCorruptStore)
	}
	return nil
}

func writeJSONExclusive(path string, value any) error {
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, ".ledger-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer func() {
		_ = temp.Close()
		_ = os.Remove(tempPath)
	}()
	if err := temp.Chmod(0o600); err != nil {
		return err
	}
	encoder := json.NewEncoder(temp)
	encoder.SetEscapeHTML(true)
	if err := encoder.Encode(value); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Link(tempPath, path); err != nil {
		if os.IsExist(err) {
			return os.ErrExist
		}
		return err
	}
	if err := syncDirectory(dir); err != nil {
		return err
	}
	return nil
}

func readJSONFile(path string, destination any) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > maxLedgerBytes {
		return fmt.Errorf("%w: invalid ledger file", ErrCorruptStore)
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	reader := bufio.NewReader(io.LimitReader(file, maxLedgerBytes+1))
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("%w: decode ledger: %v", ErrCorruptStore, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: trailing ledger data", ErrCorruptStore)
	}
	return nil
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		if runtime.GOOS == "windows" {
			return nil
		}
		return err
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil && runtime.GOOS != "windows" {
		return err
	}
	return nil
}
