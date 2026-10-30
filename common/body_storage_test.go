package common

import (
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/iotest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewReplayableBodyReaderKeepsStorageLifecycleWithCaller(t *testing.T) {
	payload := []byte(`{"model":"test-model","input":"hello"}`)
	storage, err := CreateBodyStorage(payload)
	require.NoError(t, err)
	defer storage.Close()

	body := NewReplayableBodyReader(storage)
	assert.EqualValues(t, len(payload), body.Size())
	_, exposesCloser := any(body).(io.Closer)
	assert.False(t, exposesCloser, "the request body must not expose the storage closer")

	req, err := http.NewRequest(http.MethodPost, "https://example.com", body)
	require.NoError(t, err)
	require.NoError(t, req.Body.Close())

	replayBody, err := body.NewReader()
	require.NoError(t, err, "closing the HTTP request body must not close the storage")
	replay, err := io.ReadAll(replayBody)
	require.NoError(t, err)
	require.NoError(t, replayBody.Close())
	assert.Equal(t, payload, replay)

	require.NoError(t, storage.Close())
	_, err = body.NewReader()
	require.ErrorIs(t, err, ErrStorageClosed)
}

func TestBodyStorageCursorAndCloseLifecycle(t *testing.T) {
	previous := GetDiskCacheConfig()
	t.Cleanup(func() { SetDiskCacheConfig(previous) })
	for _, disk := range []bool{false, true} {
		name := "memory"
		if disk {
			name = "disk"
		}
		t.Run(name, func(t *testing.T) {
			SetDiskCacheConfig(DiskCacheConfig{Enabled: disk, ThresholdMB: 0, MaxSizeMB: 1, Path: t.TempDir()})
			before := GetDiskCacheStats()
			storage, err := CreateBodyStorage([]byte("payload"))
			require.NoError(t, err)
			t.Cleanup(func() { _ = storage.Close() })
			assert.Equal(t, disk, storage.IsDisk())
			_, err = storage.Seek(2, io.SeekStart)
			require.NoError(t, err)
			data, err := storage.Bytes()
			require.NoError(t, err)
			assert.Equal(t, "payload", string(data))
			position, err := storage.Seek(0, io.SeekCurrent)
			require.NoError(t, err)
			assert.EqualValues(t, 2, position)
			replay, err := storage.NewReader()
			require.NoError(t, err)
			data, err = io.ReadAll(replay)
			require.NoError(t, err)
			require.NoError(t, replay.Close())
			assert.Equal(t, "payload", string(data))
			data, err = io.ReadAll(storage)
			require.NoError(t, err)
			assert.Equal(t, "yload", string(data))

			// Concurrent owners must release the accounting and file only once.
			var wg sync.WaitGroup
			for range 2 {
				wg.Go(func() { assert.NoError(t, storage.Close()) })
			}
			wg.Wait()
			_, err = storage.Read(make([]byte, 1))
			require.ErrorIs(t, err, ErrStorageClosed)
			_, err = storage.Seek(0, io.SeekStart)
			require.ErrorIs(t, err, ErrStorageClosed)
			_, err = storage.Bytes()
			require.ErrorIs(t, err, ErrStorageClosed)
			_, err = storage.NewReader()
			require.ErrorIs(t, err, ErrStorageClosed)
			after := GetDiskCacheStats()
			assert.Equal(t, before.ActiveDiskFiles, after.ActiveDiskFiles)
			assert.Equal(t, before.CurrentDiskUsageBytes, after.CurrentDiskUsageBytes)
			assert.Equal(t, before.ActiveMemoryBuffers, after.ActiveMemoryBuffers)
			assert.Equal(t, before.CurrentMemoryUsageBytes, after.CurrentMemoryUsageBytes)
			if disk {
				entries, err := os.ReadDir(GetDiskCacheDir())
				require.NoError(t, err)
				assert.Empty(t, entries)
			}
		})
	}
}

func TestDiskBodyStorageFailedConstructionCleansUp(t *testing.T) {
	previous := GetDiskCacheConfig()
	t.Cleanup(func() { SetDiskCacheConfig(previous) })
	failure := errors.New("body read failed")
	for _, tc := range []struct {
		name   string
		reader io.Reader
		want   error
	}{
		{name: "size limit", reader: strings.NewReader("too large"), want: ErrRequestBodyTooLarge},
		{name: "read failure", reader: io.MultiReader(strings.NewReader("ok"), iotest.ErrReader(failure)), want: failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			SetDiskCacheConfig(DiskCacheConfig{Enabled: true, ThresholdMB: 0, MaxSizeMB: 1, Path: t.TempDir()})
			before := GetDiskCacheStats()
			storage, err := CreateBodyStorageFromReader(tc.reader, 3, 3)
			require.ErrorIs(t, err, tc.want)
			assert.Nil(t, storage)
			entries, err := os.ReadDir(GetDiskCacheDir())
			require.NoError(t, err)
			assert.Empty(t, entries)
			assert.Equal(t, before, GetDiskCacheStats())
		})
	}
}
