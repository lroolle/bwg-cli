package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const testKey = "private_abcdefghijklmnopqrstuv"

func tempConfig(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "config.yaml")
}

// clearEnv isolates a test from whatever the developer has exported.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{EnvConfig, EnvServer, EnvVEID, EnvAPIKey, EnvAPIKeyLegacy, EnvReadOnly} {
		t.Setenv(k, "")
	}
}

// The load-bearing secret invariant: JSON output can never carry a key.
func TestServerJSONNeverCarriesTheKey(t *testing.T) {
	s := Server{Name: "tokyo", VEID: "1347645", APIKey: testKey, Tags: []string{"prod"}}

	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), testKey) {
		t.Fatalf("marshalled server leaked the API key: %s", b)
	}
	if !strings.Contains(string(b), "private_abc") {
		t.Errorf("masked key is unrecognisable: %s", b)
	}

	// Also through a container, which is how commands actually emit it.
	b, _ = json.Marshal(map[string]any{"servers": []Server{s}})
	if strings.Contains(string(b), testKey) {
		t.Fatalf("nested server leaked the API key: %s", b)
	}

	// A pointer must redact too — the fleet stores *Server.
	b, _ = json.Marshal([]*Server{&s})
	if strings.Contains(string(b), testKey) {
		t.Fatalf("*Server leaked the API key: %s", b)
	}
}

// YAML is storage, so it must keep the real key — the opposite rule.
func TestServerYAMLKeepsTheKey(t *testing.T) {
	b, err := yaml.Marshal(&Server{VEID: "1", APIKey: testKey})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), testKey) {
		t.Fatalf("YAML dropped the key, config would not round-trip: %s", b)
	}
}

func TestMaskKey(t *testing.T) {
	cases := map[string]string{
		"":                      "",
		"private_abcdefghijklm": "private_abc*******klm",
		"ab":                    "**",
		"abcdefghij":            "ab******ij",
	}
	for in, want := range cases {
		if got := MaskKey(in); got != want {
			t.Errorf("MaskKey(%q) = %q, want %q", in, got, want)
		}
	}
	// Whatever the shape, the original must not survive intact.
	for _, k := range []string{testKey, "short_key", "abcdefghij"} {
		if MaskKey(k) == k {
			t.Errorf("MaskKey(%q) returned it unchanged", k)
		}
	}
}

func TestValidate(t *testing.T) {
	good := Server{VEID: "1347645", APIKey: testKey}
	if err := good.Validate(); err != nil {
		t.Errorf("a valid server was rejected: %v", err)
	}

	bad := map[string]Server{
		"no veid":         {APIKey: testKey},
		"no key":          {VEID: "1347645"},
		"non-numeric":     {VEID: "tokyo", APIKey: testKey},
		"key with a wrap": {VEID: "1347645", APIKey: "private_abc\ndef"},
	}
	for name, s := range bad {
		if err := s.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestValidateName(t *testing.T) {
	for _, ok := range []string{"tokyo", "jp-1", "web.prod", "a_b", "x1"} {
		if err := ValidateName(ok); err != nil {
			t.Errorf("ValidateName(%q): %v", ok, err)
		}
	}
	for _, bad := range []string{"", "env", "-leading", "has space", "emoji✨", ".dot"} {
		if err := ValidateName(bad); err == nil {
			t.Errorf("ValidateName(%q) accepted", bad)
		}
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	clearEnv(t)
	path := tempConfig(t)

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Servers) != 0 {
		t.Fatal("a missing file should load as an empty fleet, not an error")
	}

	err = cfg.Add("tokyo", &Server{
		VEID: "1347645", APIKey: testKey, Note: "main", Tags: []string{"prod", "jp"},
		SSHUser: "admin", SSHPort: 2222,
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Default != "tokyo" {
		t.Errorf("the first server should become the default, got %q", cfg.Default)
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}

	// NTFS has no Unix mode bits; Go synthesizes 0666 there whatever the
	// ACL says, so this assertion only means something on Unix.
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("config written with mode %04o, want 0600 — it holds API keys", perm)
		}
	}

	back, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	s, err := back.Resolve("tokyo")
	if err != nil {
		t.Fatal(err)
	}
	if s.APIKey != testKey || s.VEID != "1347645" || s.SSHPort != 2222 || s.User() != "admin" {
		t.Errorf("round trip lost data: %+v", s)
	}
	if s.Name != "tokyo" {
		t.Errorf("Name not populated from the map key: %q", s.Name)
	}
	if !s.HasTag("PROD") {
		t.Error("HasTag should be case-insensitive")
	}
}

func TestAddRemoveDefault(t *testing.T) {
	clearEnv(t)
	cfg, _ := Load(tempConfig(t))

	cfg.Add("a", &Server{VEID: "1", APIKey: testKey}, false)
	cfg.Add("b", &Server{VEID: "2", APIKey: testKey}, false)
	if cfg.Default != "a" {
		t.Errorf("default = %q, want the first added", cfg.Default)
	}

	if err := cfg.Add("a", &Server{VEID: "3", APIKey: testKey}, false); !errors.Is(err, ErrExists) {
		t.Errorf("duplicate add returned %v, want ErrExists", err)
	}
	if err := cfg.Add("bad name", &Server{VEID: "3", APIKey: testKey}, false); err == nil {
		t.Error("an invalid name was accepted")
	}
	if err := cfg.Add("c", &Server{VEID: ""}, false); err == nil {
		t.Error("an invalid server was accepted")
	}

	cfg.Add("c", &Server{VEID: "3", APIKey: testKey}, true)
	if cfg.Default != "c" {
		t.Errorf("makeDefault ignored, default = %q", cfg.Default)
	}

	// Removing the default with several left clears it rather than
	// silently promoting an arbitrary server.
	if err := cfg.Remove("c"); err != nil {
		t.Fatal(err)
	}
	if cfg.Default != "" {
		t.Errorf("default = %q after removing it with 2 servers left, want cleared", cfg.Default)
	}

	// With exactly one left, promoting it is unambiguous.
	cfg.Remove("b")
	if cfg.Default != "a" {
		t.Errorf("default = %q, want the last remaining server", cfg.Default)
	}

	if err := cfg.Remove("gone"); !errors.Is(err, ErrNotFound) {
		t.Errorf("removing a missing server returned %v", err)
	}
	if err := cfg.SetDefault("gone"); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetDefault on a missing server returned %v", err)
	}
}

func TestResolvePrecedence(t *testing.T) {
	clearEnv(t)
	path := tempConfig(t)
	cfg, _ := Load(path)
	cfg.Add("alpha", &Server{VEID: "1", APIKey: testKey}, false)
	cfg.Add("beta", &Server{VEID: "2", APIKey: testKey}, false)
	cfg.SetDefault("beta")

	t.Run("explicit name wins", func(t *testing.T) {
		t.Setenv(EnvServer, "alpha")
		s, err := cfg.Resolve("beta")
		if err != nil || s.Name != "beta" {
			t.Errorf("got %v, %v; want beta", s, err)
		}
	})

	t.Run("BWG_SERVER beats the default", func(t *testing.T) {
		t.Setenv(EnvServer, "alpha")
		s, err := cfg.Resolve("")
		if err != nil || s.Name != "alpha" {
			t.Errorf("got %v, %v; want alpha", s, err)
		}
	})

	t.Run("env credentials beat the default", func(t *testing.T) {
		clearEnv(t)
		t.Setenv(EnvVEID, "9999")
		t.Setenv(EnvAPIKey, testKey)
		s, err := cfg.Resolve("")
		if err != nil {
			t.Fatal(err)
		}
		if s.Name != EnvServerName || s.VEID != "9999" || !s.FromEnv {
			t.Errorf("got %+v, want the environment server", s)
		}
	})

	t.Run("default when the environment is quiet", func(t *testing.T) {
		clearEnv(t)
		s, err := cfg.Resolve("")
		if err != nil || s.Name != "beta" {
			t.Errorf("got %v, %v; want beta", s, err)
		}
	})

	t.Run("unknown name is an error", func(t *testing.T) {
		clearEnv(t)
		if _, err := cfg.Resolve("gamma"); !errors.Is(err, ErrNotFound) {
			t.Errorf("got %v, want ErrNotFound", err)
		}
	})
}

func TestResolveEmptyAndAmbiguous(t *testing.T) {
	clearEnv(t)
	empty, _ := Load(tempConfig(t))
	if _, err := empty.Resolve(""); !errors.Is(err, ErrNoServers) {
		t.Errorf("empty fleet resolved to %v, want ErrNoServers", err)
	}

	one, _ := Load(tempConfig(t))
	one.Add("solo", &Server{VEID: "1", APIKey: testKey}, false)
	one.Default = "" // a hand-edited config can look like this
	s, err := one.Resolve("")
	if err != nil || s.Name != "solo" {
		t.Errorf("a single server should resolve without a default: %v, %v", s, err)
	}

	two, _ := Load(tempConfig(t))
	two.Add("a", &Server{VEID: "1", APIKey: testKey}, false)
	two.Add("b", &Server{VEID: "2", APIKey: testKey}, false)
	two.Default = ""
	_, err = two.Resolve("")
	if !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("got %v, want ErrAmbiguous", err)
	}
	// The message has to name the candidates, or it is not actionable.
	if !strings.Contains(err.Error(), "a, b") {
		t.Errorf("ambiguity error does not list the servers: %v", err)
	}
}

func TestResolveEnvByName(t *testing.T) {
	clearEnv(t)
	cfg, _ := Load(tempConfig(t))

	if _, err := cfg.Resolve(EnvServerName); !errors.Is(err, ErrNotFound) {
		t.Errorf("resolving 'env' with no env credentials returned %v", err)
	}

	t.Setenv(EnvVEID, "42")
	t.Setenv(EnvAPIKey, testKey)
	s, err := cfg.Resolve(EnvServerName)
	if err != nil || s.VEID != "42" {
		t.Errorf("got %v, %v", s, err)
	}
}

// One variable is enough when it carries both halves — the shape the
// billing portal's CSV uses.
func TestServerFromEnvAcceptsCombinedKey(t *testing.T) {
	clearEnv(t)
	t.Setenv(EnvAPIKey, "1347645:"+testKey)

	s := ServerFromEnv()
	if s == nil {
		t.Fatal("combined veid:key was not recognised")
	}
	if s.VEID != "1347645" || s.APIKey != testKey {
		t.Errorf("split wrong: veid=%q key=%q", s.VEID, s.APIKey)
	}
}

func TestServerFromEnvIgnoresNonVEIDPrefix(t *testing.T) {
	clearEnv(t)
	// A key that merely contains a colon must not be mistaken for a
	// combined pair, or the key silently loses its first segment.
	t.Setenv(EnvVEID, "1347645")
	t.Setenv(EnvAPIKey, "private:weird")

	s := ServerFromEnv()
	if s == nil {
		t.Fatal("nil with both halves present")
	}
	if s.APIKey != "private:weird" {
		t.Errorf("key mangled to %q", s.APIKey)
	}
}

func TestServerFromEnvNeedsBothHalves(t *testing.T) {
	clearEnv(t)
	if ServerFromEnv() != nil {
		t.Error("an empty environment produced a server")
	}

	t.Setenv(EnvAPIKey, testKey)
	if ServerFromEnv() != nil {
		t.Error("a key with no veid produced a server — it cannot authenticate")
	}

	clearEnv(t)
	t.Setenv(EnvVEID, "1")
	if ServerFromEnv() != nil {
		t.Error("a veid with no key produced a server")
	}
}

func TestEnvReadOnlyRequested(t *testing.T) {
	clearEnv(t)
	for _, off := range []string{"", "0", "false", "no", "off", " OFF "} {
		t.Setenv(EnvReadOnly, off)
		if EnvReadOnlyRequested() {
			t.Errorf("%q read as read-only", off)
		}
	}
	for _, on := range []string{"1", "true", "yes", "on", "anything"} {
		t.Setenv(EnvReadOnly, on)
		if !EnvReadOnlyRequested() {
			t.Errorf("%q did not read as read-only", on)
		}
	}
}

func TestListIncludesEnvAndSorts(t *testing.T) {
	clearEnv(t)
	cfg, _ := Load(tempConfig(t))
	cfg.Add("zulu", &Server{VEID: "1", APIKey: testKey}, false)
	cfg.Add("alpha", &Server{VEID: "2", APIKey: testKey}, false)

	names := func(ss []*Server) []string {
		var out []string
		for _, s := range ss {
			out = append(out, s.Name)
		}
		return out
	}
	if got := names(cfg.List()); strings.Join(got, ",") != "alpha,zulu" {
		t.Errorf("List() = %v, want sorted", got)
	}

	t.Setenv(EnvVEID, "3")
	t.Setenv(EnvAPIKey, testKey)
	if got := names(cfg.List()); strings.Join(got, ",") != "env,alpha,zulu" {
		t.Errorf("List() = %v, want the env server first", got)
	}
}

func TestFilterByTags(t *testing.T) {
	clearEnv(t)
	cfg, _ := Load(tempConfig(t))
	cfg.Add("a", &Server{VEID: "1", APIKey: testKey, Tags: []string{"prod", "jp"}}, false)
	cfg.Add("b", &Server{VEID: "2", APIKey: testKey, Tags: []string{"prod", "us"}}, false)
	cfg.Add("c", &Server{VEID: "3", APIKey: testKey}, false)

	if got := cfg.Filter(nil); len(got) != 3 {
		t.Errorf("no filter returned %d servers, want all 3", len(got))
	}
	if got := cfg.Filter([]string{"prod"}); len(got) != 2 {
		t.Errorf("tag prod matched %d, want 2", len(got))
	}
	// Multiple tags are an AND, not an OR.
	if got := cfg.Filter([]string{"prod", "jp"}); len(got) != 1 || got[0].Name != "a" {
		t.Errorf("prod+jp matched %v, want only a", got)
	}
	if got := cfg.Filter([]string{"nope"}); len(got) != 0 {
		t.Errorf("an unknown tag matched %d servers", len(got))
	}
}

func TestLoadRejectsBrokenYAML(t *testing.T) {
	clearEnv(t)
	path := tempConfig(t)
	os.WriteFile(path, []byte("servers: [this is not a map\n"), 0o600)
	if _, err := Load(path); err == nil {
		t.Fatal("broken YAML loaded without complaint")
	}
}

func TestSaveIsAtomic(t *testing.T) {
	clearEnv(t)
	path := tempConfig(t)
	cfg, _ := Load(path)
	cfg.Add("a", &Server{VEID: "1", APIKey: testKey}, false)
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	// No temp files may survive a successful save.
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".config-") {
			t.Errorf("save left a temp file behind: %s", e.Name())
		}
	}
}

func TestDefaultPathHonoursEnvironment(t *testing.T) {
	t.Setenv(EnvConfig, "/tmp/explicit.yaml")
	if got := DefaultPath(); got != "/tmp/explicit.yaml" {
		t.Errorf("DefaultPath() = %q, want the BWG_CONFIG override", got)
	}

	t.Setenv(EnvConfig, "")
	xdg := filepath.Join(t.TempDir(), "xdg")
	t.Setenv("XDG_CONFIG_HOME", xdg)
	// Built with filepath.Join so the separator is right on Windows too.
	if want := filepath.Join(xdg, "bwg", "config.yaml"); DefaultPath() != want {
		t.Errorf("DefaultPath() = %q, want %q", DefaultPath(), want)
	}
}

// The v0.1.0 spelling has to keep working — it is in people's shell
// profiles — but it must not be the one bwg talks about. See TASTE.md.
func TestLegacyAPIKeyEnvStillAuthenticates(t *testing.T) {
	clearEnv(t)
	t.Setenv(EnvVEID, "1347645")
	t.Setenv(EnvAPIKeyLegacy, testKey)

	s := ServerFromEnv()
	if s == nil {
		t.Fatal("the legacy variable stopped supplying credentials")
	}
	if s.APIKey != testKey {
		t.Errorf("key = %q, want the one from %s", s.APIKey, EnvAPIKeyLegacy)
	}
	if !LegacyAPIKeyEnv() {
		t.Error("the legacy variable was in use and went unreported")
	}
	// Messages must point at the variable that is actually in play.
	if got := APIKeyEnvVar(); got != EnvAPIKeyLegacy {
		t.Errorf("APIKeyEnvVar() = %q, want %q", got, EnvAPIKeyLegacy)
	}
}

func TestCurrentAPIKeyEnvWinsAndSilencesTheWarning(t *testing.T) {
	clearEnv(t)
	t.Setenv(EnvVEID, "1347645")
	t.Setenv(EnvAPIKey, testKey)
	t.Setenv(EnvAPIKeyLegacy, "private_stale_key_from_last_year")

	s := ServerFromEnv()
	if s == nil || s.APIKey != testKey {
		t.Fatalf("the current variable did not win: %+v", s)
	}
	// Both set is what migrating looks like; warning then would punish
	// the person who already did the work.
	if LegacyAPIKeyEnv() {
		t.Error("both variables set was reported as legacy use")
	}
	if got := APIKeyEnvVar(); got != EnvAPIKey {
		t.Errorf("APIKeyEnvVar() = %q, want %q", got, EnvAPIKey)
	}
}

func TestNoAPIKeyEnvIsNotLegacyUse(t *testing.T) {
	clearEnv(t)
	if LegacyAPIKeyEnv() {
		t.Error("an empty environment was reported as legacy use")
	}
	if got := APIKeyEnvVar(); got != EnvAPIKey {
		t.Errorf("APIKeyEnvVar() = %q, want the current name", got)
	}
}
