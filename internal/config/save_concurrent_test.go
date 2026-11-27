package config

import (
	"fmt"
	"sync"
	"testing"
)

func TestConcurrentSavesLeaveAReadableConfig(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	for w := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cfg := Default()
			// Different sizes, so overlapping writes would leave a stray tail.
			for i := range w * 20 {
				cfg.Models = append(cfg.Models, ModelOverride{ID: fmt.Sprintf("m-%d-%d", w, i), Hidden: true})
			}
			for range 40 {
				if err := Save(dir, cfg); err != nil {
					t.Error(err)
					return
				}
				if _, _, err := Load(dir); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
}
