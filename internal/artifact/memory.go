package artifact

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
)

type memoryObject struct {
	data        []byte
	contentType string
	modTime     time.Time
	etag        string
}

// MemoryBackend is an in-process Backend for tests and local simulations.
type MemoryBackend struct {
	mu      sync.RWMutex
	objects map[string]memoryObject
}

// NewMemoryBackend returns an empty in-memory object store.
func NewMemoryBackend() *MemoryBackend {
	return &MemoryBackend{objects: make(map[string]memoryObject)}
}

func (m *MemoryBackend) Put(_ context.Context, key string, body io.Reader, _ int64, contentType string) error {
	if key == "" {
		return fmt.Errorf("artifact: empty object key")
	}
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	m.mu.Lock()
	m.objects[key] = memoryObject{
		data:        data,
		contentType: contentType,
		modTime:     time.Now().UTC(),
		etag:        fmt.Sprintf("%q", fmt.Sprintf("%x", sum[:16])),
	}
	m.mu.Unlock()
	return nil
}

func (m *MemoryBackend) Get(_ context.Context, key string) (Object, error) {
	m.mu.RLock()
	obj, ok := m.objects[key]
	m.mu.RUnlock()
	if !ok {
		return Object{}, fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	data := append([]byte(nil), obj.data...)
	return Object{
		Body:        io.NopCloser(bytes.NewReader(data)),
		Size:        int64(len(data)),
		ModTime:     obj.modTime,
		ETag:        obj.etag,
		ContentType: obj.contentType,
	}, nil
}

func (m *MemoryBackend) Copy(_ context.Context, srcKey, dstKey string) error {
	m.mu.RLock()
	obj, ok := m.objects[srcKey]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, srcKey)
	}
	obj.data = append([]byte(nil), obj.data...)
	m.mu.Lock()
	m.objects[dstKey] = obj
	m.mu.Unlock()
	return nil
}

func (m *MemoryBackend) DeletePrefix(_ context.Context, prefix string) error {
	m.mu.Lock()
	for key := range m.objects {
		if key == prefix || strings.HasPrefix(key, prefix+"/") {
			delete(m.objects, key)
		}
	}
	m.mu.Unlock()
	return nil
}

// List returns every object under prefix, in key order. Keys are compared the
// same way DeletePrefix does, so the two agree on what a prefix contains.
func (m *MemoryBackend) List(_ context.Context, prefix string) ([]ObjectInfo, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]ObjectInfo, 0)
	for key, obj := range m.objects {
		if key == prefix || strings.HasPrefix(key, prefix+"/") {
			out = append(out, ObjectInfo{Key: key, ModTime: obj.modTime})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}
