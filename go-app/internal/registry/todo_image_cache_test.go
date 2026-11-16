package registry

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTODOImageCacheBoundsGalleryGitReadsAndCoalescesDuplicates(t *testing.T) {
	var cache todoImageCache
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var active, maximum, fetches atomic.Int32
	var wg sync.WaitGroup
	errors := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			key := fmt.Sprintf("image-%d", index%16)
			_, err := cache.load(ctx, key, func() (todoImageFile, error) {
				fetches.Add(1)
				count := active.Add(1)
				for old := maximum.Load(); count > old && !maximum.CompareAndSwap(old, count); old = maximum.Load() {
				}
				time.Sleep(15 * time.Millisecond)
				active.Add(-1)
				return todoImageFile{Data: []byte(key), ContentType: "image/png"}, nil
			})
			if err != nil {
				errors <- err
			}
		}(i)
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	if maximum.Load() > todoImageGitConcurrency || fetches.Load() != 16 {
		t.Fatalf("Git load was not bounded/coalesced: max=%d fetches=%d", maximum.Load(), fetches.Load())
	}
}
