// Package artifact stores immutable build snapshots behind a small tree
// abstraction. A session's current build is selected by a pointer object, so
// uploads never expose a partially-written bundle and every API replica sees
// the same preview/published version through a shared backend.
package artifact

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// ErrNotFound reports that a pointer or object does not exist.
var ErrNotFound = errors.New("artifact not found")

// Object is one stored build file.
type Object struct {
	Body        io.ReadCloser
	Size        int64
	ModTime     time.Time
	ETag        string
	ContentType string
}

// Backend is the low-level object API shared by COS and tests.
type Backend interface {
	Put(ctx context.Context, key string, body io.Reader, size int64, contentType string) error
	Get(ctx context.Context, key string) (Object, error)
	Copy(ctx context.Context, srcKey, dstKey string) error
	DeletePrefix(ctx context.Context, prefix string) error
}

// Store adds immutable build directories and a current pointer on top of a
// Backend. Prefixes are opaque, slash-separated object namespaces.
type Store struct {
	backend  Backend
	seq      atomic.Int64
	instance uint64
	now      func() time.Time
}

// New wraps a backend in the build-tree store.
func New(backend Backend) *Store {
	if backend == nil {
		panic("artifact.New: nil backend")
	}
	// A per-process random salt makes build IDs unique across replicas even
	// when two processes call StageTree in the same nanosecond.
	var seed [8]byte
	instance := uint64(time.Now().UnixNano()) ^ uint64(os.Getpid())
	if _, err := rand.Read(seed[:]); err == nil {
		instance = binary.LittleEndian.Uint64(seed[:])
	}
	return &Store{backend: backend, seq: atomic.Int64{}, instance: instance, now: time.Now}
}

// SessionPrefix is the object namespace for a session's official app.
func SessionPrefix(id string) string {
	return "sessions/" + id
}

// ContestPrefix is the object namespace for one contestant's archived build.
func ContestPrefix(id, provider string) string {
	return path.Join(SessionPrefix(id), "contest", provider)
}

type pointer struct {
	ID string    `json:"id"`
	At time.Time `json:"at"`
}

type manifest struct {
	Files []manifestFile `json:"files"`
}

type manifestFile struct {
	Path        string `json:"path"`
	Size        int64  `json:"size"`
	ContentType string `json:"contentType"`
}

func pointerKey(prefix string) string { return path.Join(prefix, "current.json") }
func manifestKey(prefix, id string) string {
	return path.Join(prefix, "manifests", id+".json")
}
func buildPrefix(prefix, id string) string {
	return path.Join(prefix, "builds", id)
}

// StageTree uploads a complete immutable build without changing the current
// pointer. The returned id can be activated only after the caller has
// durably recorded the publish that will reference it.
func (s *Store) StageTree(ctx context.Context, prefix, dir string) (string, error) {
	if err := validPrefix(prefix); err != nil {
		return "", err
	}
	id := s.newID()
	bp := buildPrefix(prefix, id)
	mk := manifestKey(prefix, id)
	files := make([]manifestFile, 0, 8)
	err := filepath.WalkDir(dir, func(filePath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("artifact: refusing symlink %s", filePath)
		}
		rel, err := filepath.Rel(dir, filePath)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if err := validRelative(rel); err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		f, err := os.Open(filePath)
		if err != nil {
			return err
		}
		ct := contentType(rel)
		putErr := s.backend.Put(ctx, path.Join(bp, rel), f, info.Size(), ct)
		closeErr := f.Close()
		if putErr != nil {
			return putErr
		}
		if closeErr != nil {
			return closeErr
		}
		files = append(files, manifestFile{Path: rel, Size: info.Size(), ContentType: ct})
		return nil
	})
	if err != nil {
		_ = s.backend.DeletePrefix(ctx, bp)
		return "", err
	}
	raw, err := json.Marshal(manifest{Files: files})
	if err != nil {
		_ = s.backend.DeletePrefix(ctx, bp)
		return "", err
	}
	if err := s.backend.Put(ctx, mk, strings.NewReader(string(raw)), int64(len(raw)), "application/json"); err != nil {
		_ = s.backend.DeletePrefix(ctx, bp)
		return "", err
	}
	return id, nil
}

// Activate atomically points a prefix at a previously staged build.
func (s *Store) Activate(ctx context.Context, prefix, id string) error {
	if err := validPrefix(prefix); err != nil {
		return err
	}
	if !validID(id) {
		return fmt.Errorf("artifact: invalid build id %q", id)
	}
	raw, err := json.Marshal(pointer{ID: id, At: s.now().UTC()})
	if err != nil {
		return err
	}
	return s.backend.Put(ctx, pointerKey(prefix), strings.NewReader(string(raw)), int64(len(raw)), "application/json")
}

// PutTree stages and activates a build in one step.
func (s *Store) PutTree(ctx context.Context, prefix, dir string) (string, error) {
	id, err := s.StageTree(ctx, prefix, dir)
	if err != nil {
		return "", err
	}
	if err := s.Activate(ctx, prefix, id); err != nil {
		s.discardStaged(ctx, prefix, id)
		return "", err
	}
	return id, nil
}

// Current returns the active immutable build id for prefix.
func (s *Store) Current(ctx context.Context, prefix string) (string, bool, error) {
	if err := validPrefix(prefix); err != nil {
		return "", false, err
	}
	obj, err := s.backend.Get(ctx, pointerKey(prefix))
	if errors.Is(err, ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	defer obj.Body.Close()
	raw, err := io.ReadAll(obj.Body)
	if err != nil {
		return "", false, err
	}
	var ref pointer
	if err := json.Unmarshal(raw, &ref); err != nil {
		return "", false, fmt.Errorf("artifact: decode current pointer: %w", err)
	}
	if !validID(ref.ID) {
		return "", false, fmt.Errorf("artifact: invalid current build id %q", ref.ID)
	}
	return ref.ID, true, nil
}

// Open resolves the current build and opens one relative file.
func (s *Store) Open(ctx context.Context, prefix, rel string) (Object, string, error) {
	id, ok, err := s.Current(ctx, prefix)
	if err != nil {
		return Object{}, "", err
	}
	if !ok {
		return Object{}, "", ErrNotFound
	}
	if err := validRelative(rel); err != nil {
		return Object{}, "", err
	}
	obj, err := s.backend.Get(ctx, path.Join(buildPrefix(prefix, id), rel))
	if err != nil {
		return Object{}, "", err
	}
	return obj, id, nil
}

// CopyTree copies the active build of srcPrefix to dstPrefix and activates
// the copy. It is used when a contest winner becomes the official app.
func (s *Store) CopyTree(ctx context.Context, srcPrefix, dstPrefix string) (string, error) {
	if err := validPrefix(srcPrefix); err != nil {
		return "", err
	}
	if err := validPrefix(dstPrefix); err != nil {
		return "", err
	}
	if srcPrefix == dstPrefix {
		return "", fmt.Errorf("artifact: source and destination prefixes are identical")
	}
	srcID, ok, err := s.Current(ctx, srcPrefix)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", ErrNotFound
	}
	mk, err := s.backend.Get(ctx, manifestKey(srcPrefix, srcID))
	if err != nil {
		return "", err
	}
	defer mk.Body.Close()
	raw, err := io.ReadAll(mk.Body)
	if err != nil {
		return "", err
	}
	var m manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return "", fmt.Errorf("artifact: decode manifest: %w", err)
	}
	dstID := s.newID()
	dp := buildPrefix(dstPrefix, dstID)
	for _, f := range m.Files {
		if err := validRelative(f.Path); err != nil {
			_ = s.backend.DeletePrefix(ctx, dp)
			return "", err
		}
		if err := s.backend.Copy(ctx, path.Join(buildPrefix(srcPrefix, srcID), f.Path), path.Join(dp, f.Path)); err != nil {
			_ = s.backend.DeletePrefix(ctx, dp)
			return "", err
		}
	}
	dmk := manifestKey(dstPrefix, dstID)
	if err := s.backend.Put(ctx, dmk, strings.NewReader(string(raw)), int64(len(raw)), "application/json"); err != nil {
		_ = s.backend.DeletePrefix(ctx, dp)
		return "", err
	}
	if err := s.Activate(ctx, dstPrefix, dstID); err != nil {
		_ = s.backend.DeletePrefix(ctx, dp)
		_ = s.backend.DeletePrefix(ctx, dmk)
		return "", err
	}
	return dstID, nil
}

// ClearCurrent removes the active official build and its pointer while
// preserving sibling namespaces such as contest archives.
func (s *Store) ClearCurrent(ctx context.Context, prefix string) error {
	if err := validPrefix(prefix); err != nil {
		return err
	}
	id, ok, err := s.Current(ctx, prefix)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	// Remove the pointer first so /app stops serving before cleanup.
	if err := s.backend.DeletePrefix(ctx, pointerKey(prefix)); err != nil {
		return err
	}
	var errs []error
	if err := s.backend.DeletePrefix(ctx, buildPrefix(prefix, id)); err != nil {
		errs = append(errs, err)
	}
	if err := s.backend.DeletePrefix(ctx, manifestKey(prefix, id)); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// DeletePrefix removes all immutable builds and the current pointer under a
// session/contest prefix.
func (s *Store) DeletePrefix(ctx context.Context, prefix string) error {
	if err := validPrefix(prefix); err != nil {
		return err
	}
	return s.backend.DeletePrefix(ctx, prefix)
}

func (s *Store) discardStaged(ctx context.Context, prefix, id string) {
	_ = s.backend.DeletePrefix(ctx, buildPrefix(prefix, id))
	_ = s.backend.DeletePrefix(ctx, manifestKey(prefix, id))
}

func (s *Store) newID() string {
	n := s.seq.Add(1)
	return fmt.Sprintf("%020d%020d%010d", s.instance, uint64(s.now().UnixNano()), n)
}

func validPrefix(prefix string) error {
	if prefix == "" || path.IsAbs(prefix) || strings.Contains(prefix, "\\") {
		return fmt.Errorf("artifact: invalid prefix %q", prefix)
	}
	clean := path.Clean(prefix)
	if clean == "." || clean != prefix || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("artifact: invalid prefix %q", prefix)
	}
	return nil
}

func validRelative(rel string) error {
	if rel == "" || path.IsAbs(rel) || strings.Contains(rel, "\\") {
		return fmt.Errorf("artifact: invalid path %q", rel)
	}
	clean := path.Clean(rel)
	if clean == "." || clean != rel || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("artifact: invalid path %q", rel)
	}
	return nil
}

func validID(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func contentType(rel string) string {
	if ct := mime.TypeByExtension(path.Ext(rel)); ct != "" {
		return ct
	}
	return "application/octet-stream"
}
