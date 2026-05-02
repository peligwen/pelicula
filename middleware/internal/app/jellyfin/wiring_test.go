package jellyfin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"pelicula-api/internal/app/services"
	"pelicula-api/internal/config"
)

// TestAuth_NoTornReadUnderConcurrentRewrite stresses the .env mutex contract:
// Auth() must hold EnvMu while reading, so concurrent rewrites cannot leak a
// torn read. Production semantics matter — settings.WriteEnvFile uses direct
// os.WriteFile (no tmp+rename) because the .env is bind-mounted into the
// container, so the read genuinely can land mid-write. The mutex is the only
// thing serializing read vs write.
//
// We mirror those production semantics in the test fixture: write a long
// distinguishable string in two snapshots, and assert every read sees one
// snapshot in full — never a mix.
func TestAuth_NoTornReadUnderConcurrentRewrite(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")

	// Make snapshots big enough that a partial write is likely to be observed
	// without the mutex (kernel write() may not be atomic for files past
	// PIPE_BUF). 4096 bytes per token works on macOS/Linux dev hosts.
	tokenA := strings.Repeat("A", 4096)
	tokenB := strings.Repeat("B", 4096)

	// Mirror production: direct write, not tmp+rename.
	writeEnv := func(token string) error {
		return os.WriteFile(envPath, []byte("JELLYFIN_API_KEY="+token+"\n"), 0644)
	}
	if err := writeEnv(tokenA); err != nil {
		t.Fatalf("seed env: %v", err)
	}

	parseEnv := func(p string) (map[string]string, error) {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		s := string(data)
		if len(s) > 0 && s[len(s)-1] == '\n' {
			s = s[:len(s)-1]
		}
		out := map[string]string{}
		for i := 0; i < len(s); i++ {
			if s[i] == '=' {
				out[s[:i]] = s[i+1:]
				break
			}
		}
		return out, nil
	}

	var mu sync.Mutex
	svc := services.New(&config.Config{}, "")
	w := NewWirer(svc, "http://jellyfin:8096", envPath, func() string { return "" }, parseEnv, nil, &mu)

	const goroutines = 8
	const iters = 200

	var torn atomic.Int64
	var read atomic.Int64
	stop := make(chan struct{})

	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		toggle := false
		for {
			select {
			case <-stop:
				return
			default:
			}
			toggle = !toggle
			tok := tokenA
			if toggle {
				tok = tokenB
			}
			mu.Lock()
			_ = writeEnv(tok)
			mu.Unlock()
		}
	}()

	var readerWG sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		readerWG.Add(1)
		go func() {
			defer readerWG.Done()
			for i := 0; i < iters; i++ {
				svc.SetJellyfinAPIKey("") // force the .env-read path
				token, err := w.Auth(context.Background())
				read.Add(1)
				if err != nil {
					torn.Add(1)
					continue
				}
				// Token must be exactly tokenA or tokenB — never a partial /
				// interleaved one.
				if token != tokenA && token != tokenB {
					torn.Add(1)
				}
			}
		}()
	}

	readerWG.Wait()
	close(stop)
	<-writerDone

	if read.Load() == 0 {
		t.Fatal("test bug: no reads recorded")
	}
	if torn.Load() != 0 {
		t.Fatalf("observed %d/%d torn/error reads under concurrent .env rewrite (mutex not protecting Auth())", torn.Load(), read.Load())
	}
}
