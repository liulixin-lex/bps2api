package basispoints

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestScaleImagePendingCloseUnblocksWaiter(t *testing.T) {
	r, err := NewImageRelay("https://images.example", t.TempDir(), ImageRelayOptions{MaxEntries: 1})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, r.Close()) })
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(relayTestPNG(t))
	owner, _, err := r.storeImage(dataURL, "scope")
	require.NoError(t, err)
	defer func() { r.mu.Lock(); r.releaseStagedLocked(owner); r.mu.Unlock() }()
	// Hold the publication barrier explicitly: this reproduces a slow file
	// decode without relying on filesystem timing or large allocations.
	r.mu.Lock()
	pending := owner.reused
	pending.ready = make(chan struct{})
	r.mu.Unlock()
	result := make(chan error, 1)
	go func() {
		borrow, _, err := r.storeImage(dataURL, "scope")
		if borrow != nil {
			r.mu.Lock()
			r.releaseStagedLocked(borrow)
			r.mu.Unlock()
		}
		result <- err
	}()
	require.Eventually(t, func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return pending.pins == 2
	}, 5*time.Second, time.Millisecond)
	select {
	case err := <-result:
		t.Fatalf("borrower returned before publication or Close: %v", err)
	default:
	}
	require.NoError(t, r.Close())
	select {
	case err := <-result:
		require.ErrorIs(t, err, ErrImageRelayStorage)
	case <-time.After(5 * time.Second):
		t.Fatal("Close failed to unblock a pending image borrower")
	}
	r.mu.Lock()
	r.releaseStagedLocked(owner)
	r.mu.Unlock()
	require.Empty(t, r.pending)
	require.Zero(t, pending.pins)
	require.Zero(t, r.reservedEntries)
	require.Zero(t, r.reservedBytes)
	_, err = os.Stat(r.dir)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestScaleImagePendingMIMEFailuresDoNotPoisonValidBorrowers(t *testing.T) {
	r, err := NewImageRelay("https://images.example", t.TempDir(), ImageRelayOptions{MaxEntries: 1})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, r.Close()) })
	data := make([]byte, 2<<20)
	copy(data, relayTestPNG(t))
	payload := base64.StdEncoding.EncodeToString(data)
	start := make(chan struct{})
	type outcome struct {
		img   *relayImage
		token string
		err   error
		mime  string
	}
	results := make(chan outcome, 48)
	var workers sync.WaitGroup
	for i := 0; i < cap(results); i++ {
		declared := []string{"image/png", "application/octet-stream", "image/jpeg"}[i%3]
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			img, token, err := r.storeImage("data:"+declared+";base64,"+payload, "scope")
			results <- outcome{img: img, token: token, err: err, mime: declared}
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	var handles []*relayImage
	defer func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		for _, img := range handles {
			r.releaseStagedLocked(img)
		}
	}()
	var token string
	for result := range results {
		if result.img != nil {
			handles = append(handles, result.img)
		}
		if result.mime == "image/jpeg" {
			require.ErrorContains(t, result.err, "media type does not match")
			require.Nil(t, result.img)
			continue
		}
		require.NoError(t, result.err)
		if token == "" {
			token = result.token
		}
		require.Equal(t, token, result.token)
	}
	require.Len(t, handles, 32)
	require.Len(t, r.pending, 1)
	require.Empty(t, r.entries)
	require.Equal(t, 1, r.reservedEntries)
	require.Equal(t, 32, handles[0].reused.pins)
	files, err := os.ReadDir(r.dir)
	require.NoError(t, err)
	require.Len(t, files, 1)
	r.mu.Lock()
	for _, img := range handles {
		r.releaseStagedLocked(img)
	}
	r.mu.Unlock()
	require.Empty(t, r.pending)
	require.Zero(t, r.reservedEntries)
	require.Zero(t, r.reservedBytes)
	files, err = os.ReadDir(r.dir)
	require.NoError(t, err)
	require.Empty(t, files)
}

func TestScaleImagePendingDecodeFailureReleasesAllReservations(t *testing.T) {
	r, err := NewImageRelay("https://images.example", t.TempDir(), ImageRelayOptions{MaxEntries: 1})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, r.Close()) })
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(make([]byte, 1<<20))
	var workers sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 32; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			img, _, err := r.storeImage(dataURL, "scope")
			if err == nil || errors.Is(err, ErrImageRelayFull) || !strings.Contains(err.Error(), "inline image is invalid") {
				t.Errorf("invalid image must fail decoding without capacity errors: %v", err)
			}
			if img != nil {
				r.mu.Lock()
				r.releaseStagedLocked(img)
				r.mu.Unlock()
			}
		}()
	}
	close(start)
	workers.Wait()
	require.Empty(t, r.pending)
	require.Empty(t, r.entries)
	require.Zero(t, r.reservedEntries)
	require.Zero(t, r.reservedBytes)
	files, err := os.ReadDir(r.dir)
	require.NoError(t, err)
	require.Empty(t, files)
}

func TestScaleImagePendingInvalidBatchPreservesOtherOwner(t *testing.T) {
	r, err := NewImageRelay("https://images.example", t.TempDir(), ImageRelayOptions{MaxEntries: 1})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, r.Close()) })
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(relayTestPNG(t))
	owner, _, err := r.storeImage(dataURL, "scope")
	require.NoError(t, err)
	defer func() { r.mu.Lock(); r.releaseStagedLocked(owner); r.mu.Unlock() }()
	raw, err := json.Marshal(object{"input": []any{object{"role": "user", "content": []any{object{"type": "input_image", "image_url": dataURL, "detail": "invalid-detail"}}}}})
	require.NoError(t, err)
	_, err = r.Rewrite(raw, "scope")
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrImageRelayFull)
	require.Equal(t, 1, owner.reused.pins)
	require.Equal(t, 1, r.reservedEntries)
	_, err = os.Stat(owner.path)
	require.NoError(t, err)
}
