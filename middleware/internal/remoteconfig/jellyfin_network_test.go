package remoteconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteJellyfinNetworkXML_WithPublishedURL(t *testing.T) {
	dir := t.TempDir()
	if err := WriteJellyfinNetworkXML(dir, "http://192.168.1.42:7354/jellyfin"); err != nil {
		t.Fatalf("WriteJellyfinNetworkXML: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "network.xml"))
	if err != nil {
		t.Fatalf("read network.xml: %v", err)
	}
	got := string(data)

	if !strings.Contains(got, "<PublishedServerUrl>http://192.168.1.42:7354/jellyfin</PublishedServerUrl>") {
		t.Errorf("network.xml missing PublishedServerUrl element:\n%s", got)
	}
	if !strings.Contains(got, "<BaseUrl>/jellyfin</BaseUrl>") {
		t.Errorf("network.xml missing BaseUrl:\n%s", got)
	}
}

func TestWriteJellyfinNetworkXML_EmptyURLOmitsElement(t *testing.T) {
	dir := t.TempDir()
	if err := WriteJellyfinNetworkXML(dir, ""); err != nil {
		t.Fatalf("WriteJellyfinNetworkXML: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "network.xml"))
	if err != nil {
		t.Fatalf("read network.xml: %v", err)
	}
	got := string(data)

	if strings.Contains(got, "<PublishedServerUrl>") {
		t.Errorf("network.xml should NOT contain PublishedServerUrl when URL is empty:\n%s", got)
	}
	if !strings.Contains(got, "<BaseUrl>/jellyfin</BaseUrl>") {
		t.Errorf("network.xml missing BaseUrl:\n%s", got)
	}
}

func TestWriteJellyfinNetworkXML_EscapesXMLSpecials(t *testing.T) {
	dir := t.TempDir()
	tricky := `http://example.com/?q=<a&b="c">`
	if err := WriteJellyfinNetworkXML(dir, tricky); err != nil {
		t.Fatalf("WriteJellyfinNetworkXML: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "network.xml"))
	got := string(data)
	if strings.Contains(got, "<a&b=") {
		t.Errorf("XML special chars not escaped:\n%s", got)
	}
	if !strings.Contains(got, "&lt;a&amp;b=&quot;c&quot;&gt;") {
		t.Errorf("expected escaped form in network.xml:\n%s", got)
	}
}

func TestWriteJellyfinNetworkXML_CreatesDirectory(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "fresh", "jellyfin")
	if err := WriteJellyfinNetworkXML(target, "http://10.0.0.1:7354/jellyfin"); err != nil {
		t.Fatalf("WriteJellyfinNetworkXML: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "network.xml")); err != nil {
		t.Errorf("expected network.xml under created dir: %v", err)
	}
}

func TestWriteJellyfinNetworkXML_RejectsEmptyDir(t *testing.T) {
	if err := WriteJellyfinNetworkXML("", "http://x"); err == nil {
		t.Error("expected error for empty config dir")
	}
}
