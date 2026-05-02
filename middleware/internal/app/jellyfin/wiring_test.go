package jellyfin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	jfclient "pelicula-api/internal/clients/jellyfin"

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
	w := NewWirer(svc, "http://jellyfin:8096", envPath, "eng", func() string { return "" }, parseEnv, nil, &mu)

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

// TestSetAudioPref_UsesProvidedLang proves that SetAudioPref uses the lang
// parameter and never reads PELICULA_AUDIO_LANG from the environment.
func TestSetAudioPref_UsesProvidedLang(t *testing.T) {
	t.Setenv("PELICULA_AUDIO_LANG", "deu") // misleading env value — must be ignored

	const uid = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	var capturedBody []byte

	mux := http.NewServeMux()
	mux.HandleFunc("/Users/"+uid, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"Id":"` + uid + `","Configuration":{}}`)) //nolint:errcheck
	})
	mux.HandleFunc("/Users/"+uid+"/Configuration", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var err error
		capturedBody, err = io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client := jfclient.NewWithHTTPClient(srv.URL, srv.Client())
	SetAudioPref(context.Background(), client, "token", uid, "spa")

	if capturedBody == nil {
		t.Fatal("no POST body recorded for /Configuration")
	}
	var cfg map[string]any
	if err := json.Unmarshal(capturedBody, &cfg); err != nil {
		t.Fatalf("invalid JSON in POST body: %v", err)
	}
	lang, _ := cfg["AudioLanguagePreference"].(string)
	if lang != "spa" {
		t.Errorf("AudioLanguagePreference = %q, want %q (env value 'deu' must be ignored)", lang, "spa")
	}
}

// TestWirer_PassesAudioLangFromConfig drives Wirer.CreateUser with AudioLang="fra"
// and asserts that "fra" reaches the /Configuration POST — not any env value.
func TestWirer_PassesAudioLangFromConfig(t *testing.T) {
	const uid = "bbbbbbbb-cccc-dddd-eeee-ffffffffffff"
	var capturedBody []byte

	mux := http.NewServeMux()
	mux.HandleFunc("/Users/New", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"Id":"` + uid + `"}`)) //nolint:errcheck
	})
	mux.HandleFunc("/Users/"+uid+"/Password", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/Users/"+uid, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"Id":"` + uid + `","Configuration":{}}`)) //nolint:errcheck
	})
	mux.HandleFunc("/Users/"+uid+"/Configuration", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var err error
		capturedBody, err = io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	var mu sync.Mutex
	svc := services.New(&config.Config{}, "")
	svc.SetJellyfinAPIKey("test-key")
	w := NewWirer(svc, srv.URL, "", "fra", func() string { return "key" }, nil, nil, &mu)
	w.Services = svc

	if _, err := w.CreateUser(context.Background(), "testuser", "testpass"); err != nil {
		t.Fatalf("CreateUser failed: %v", err)
	}

	if capturedBody == nil {
		t.Fatal("no POST body recorded for /Configuration")
	}
	var cfg map[string]any
	if err := json.Unmarshal(capturedBody, &cfg); err != nil {
		t.Fatalf("invalid JSON in POST body: %v", err)
	}
	lang, _ := cfg["AudioLanguagePreference"].(string)
	if lang != "fra" {
		t.Errorf("AudioLanguagePreference = %q, want %q", lang, "fra")
	}
}
