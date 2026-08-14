// Package config holds the fleet: the set of BandwagonHost VPS
// instances bwg knows about, and the rules for deciding which one a
// command means.
//
// KiwiVM has no account-level API — every call needs the (veid,
// api_key) pair for one specific VPS — so a fleet is unavoidably a
// list of credential pairs. That makes secret handling the central
// concern of this package:
//
//   - YAML is the storage format and carries real keys, written 0600.
//   - JSON is an output format and never carries a key. [Server]
//     implements MarshalJSON to redact, so a --json path cannot leak a
//     credential even by accident.
package config

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Environment variables the fleet reads.
//
// The pair a command needs is BWG_VEID and BWG_API_KEY. They are named
// as a pair on purpose: they are useless apart, and a credential whose
// two halves are spelled in different styles is one people copy wrong.
const (
	// EnvConfig overrides the config file location.
	EnvConfig = "BWG_CONFIG"
	// EnvServer names the server to act on.
	EnvServer = "BWG_SERVER"
	// EnvVEID supplies a VPS ID without a config file.
	EnvVEID = "BWG_VEID"
	// EnvAPIKey supplies an API key without a config file. It also
	// accepts a combined "veid:api_key", which is how the billing
	// portal's CSV pairs them.
	EnvAPIKey = "BWG_API_KEY"
	// EnvAPIKeyLegacy is the v0.1.0 spelling, which echoed the panel's
	// "KiwiVM API key" label. It still works — someone's shell profile
	// has it — but it is not documented anywhere and bwg says so once
	// per run. See TASTE.md; remove it at v1.0.
	EnvAPIKeyLegacy = "BWG_KIWIVM_API_KEY"
	// EnvReadOnly, when truthy, forces every client read-only.
	EnvReadOnly = "BWG_READ_ONLY"
)

// EnvServerName is the name given to credentials that come from the
// environment rather than the config file.
const EnvServerName = "env"

var (
	// ErrNoServers means nothing is configured and the environment is
	// empty — the first-run state.
	ErrNoServers = errors.New("no servers configured")
	// ErrAmbiguous means several servers exist and none was chosen.
	ErrAmbiguous = errors.New("several servers configured and no default set")
	// ErrNotFound means the named server is not in the fleet.
	ErrNotFound = errors.New("server not found")
	// ErrExists means the name is already taken.
	ErrExists = errors.New("server already exists")
)

// Server is one VPS and the credentials to reach it.
type Server struct {
	// VEID is the numeric VPS ID from the KiwiVM panel.
	VEID string `yaml:"veid"`
	// APIKey is the per-VPS KiwiVM API key, normally "private_...".
	APIKey string `yaml:"api_key"`
	// Note is a human label, shown in listings.
	Note string `yaml:"note,omitempty"`
	// Tags group servers for fleet-wide filtering.
	Tags []string `yaml:"tags,omitempty"`
	// Endpoint overrides the KiwiVM API root for this server.
	Endpoint string `yaml:"endpoint,omitempty"`
	// SSHUser is the login `bwg ssh` uses. Defaults to root.
	SSHUser string `yaml:"ssh_user,omitempty"`
	// SSHPort overrides the port `bwg ssh` uses. Zero means ask the
	// API, which reports the real port even when it is not 22.
	SSHPort int `yaml:"ssh_port,omitempty"`

	// Name is the fleet key. It is set on load, not stored.
	Name string `yaml:"-"`
	// FromEnv marks credentials that came from the environment.
	FromEnv bool `yaml:"-"`
}

// publicServer is the shape [Server] marshals to. It exists so that
// adding a field to Server cannot silently start leaking it: a new
// secret has to be added here deliberately to appear in JSON.
type publicServer struct {
	Name     string   `json:"name"`
	VEID     string   `json:"veid"`
	APIKey   string   `json:"apiKey"` // masked
	Note     string   `json:"note,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	Endpoint string   `json:"endpoint,omitempty"`
	SSHUser  string   `json:"sshUser,omitempty"`
	SSHPort  int      `json:"sshPort,omitempty"`
	FromEnv  bool     `json:"fromEnv,omitempty"`
}

// MarshalJSON renders the server with its API key masked. Every JSON
// output path in bwg goes through this, which is what makes "--json
// never prints a secret" a property of the type rather than a rule
// each command has to remember.
func (s Server) MarshalJSON() ([]byte, error) {
	return json.Marshal(publicServer{
		Name: s.Name, VEID: s.VEID, APIKey: MaskKey(s.APIKey),
		Note: s.Note, Tags: s.Tags, Endpoint: s.Endpoint,
		SSHUser: s.SSHUser, SSHPort: s.SSHPort, FromEnv: s.FromEnv,
	})
}

// User returns the SSH login to use, defaulting to root.
func (s Server) User() string {
	if s.SSHUser != "" {
		return s.SSHUser
	}
	return "root"
}

// HasTag reports whether the server carries a tag, case-insensitively.
func (s Server) HasTag(tag string) bool {
	for _, t := range s.Tags {
		if strings.EqualFold(t, tag) {
			return true
		}
	}
	return false
}

// Validate reports why a server entry is unusable, if it is.
func (s Server) Validate() error {
	if strings.TrimSpace(s.VEID) == "" {
		return errors.New("veid is required — the VPS ID number in the KiwiVM panel URL after ?veid=")
	}
	if _, err := strconv.Atoi(strings.TrimSpace(s.VEID)); err != nil {
		return fmt.Errorf("veid %q is not a number", s.VEID)
	}
	if strings.TrimSpace(s.APIKey) == "" {
		return errors.New("api_key is required — copy it from KiwiVM > API")
	}
	if strings.ContainsAny(s.APIKey, " \t\n\r") {
		return errors.New("api_key contains whitespace — it was probably copied with a line break")
	}
	return nil
}

// MaskKey renders an API key safe to print: enough to recognise which
// key it is, not enough to use.
func MaskKey(key string) string {
	if key == "" {
		return ""
	}
	// KiwiVM keys look like "private_<24 chars>"; keep the prefix so a
	// malformed key is still recognisable as malformed.
	prefix, rest, ok := strings.Cut(key, "_")
	if !ok || len(rest) < 8 {
		if len(key) <= 4 {
			return strings.Repeat("*", len(key))
		}
		return key[:2] + strings.Repeat("*", len(key)-4) + key[len(key)-2:]
	}
	return prefix + "_" + rest[:3] + strings.Repeat("*", len(rest)-6) + rest[len(rest)-3:]
}

// Config is the on-disk fleet.
type Config struct {
	// Default names the server commands act on when none is given.
	Default string `yaml:"default,omitempty"`
	// Servers is the fleet, keyed by name.
	Servers map[string]*Server `yaml:"servers"`

	path string
}

// DefaultPath returns the config file location, honouring BWG_CONFIG
// and XDG_CONFIG_HOME.
func DefaultPath() string {
	if p := os.Getenv(EnvConfig); p != "" {
		return p
	}
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "bwg", "config.yaml")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".bwg", "config.yaml")
	}
	return filepath.Join(home, ".config", "bwg", "config.yaml")
}

// Load reads the config at path, or [DefaultPath] when path is empty.
// A missing file is not an error: it yields an empty fleet, so
// first-run and env-only use need no setup.
func Load(path string) (*Config, error) {
	if path == "" {
		path = DefaultPath()
	}
	cfg := &Config{Servers: map[string]*Server{}, path: path}

	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if cfg.Servers == nil {
		cfg.Servers = map[string]*Server{}
	}
	for name, s := range cfg.Servers {
		s.Name = name
	}
	cfg.path = path

	warnIfWorldReadable(path)
	return cfg, nil
}

// warnIfWorldReadable says so when the file holding API keys is open to
// other users. It is a warning, not a refusal: locking someone out of
// their own fleet over a permission bit helps nobody.
//
// Skipped on Windows, where Go reports a synthesized 0666 for every
// file regardless of its ACL. Warning there would fire on every single
// run and recommend `chmod`, which does not exist — noise that trains
// people to ignore the one case that matters.
func warnIfWorldReadable(path string) {
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm()&0o077 == 0 {
		return
	}
	fmt.Fprintf(os.Stderr,
		"warning: %s is readable by other users (mode %04o); run: chmod 600 %s\n",
		path, info.Mode().Perm(), path)
}

// Path returns the file this config loads from and saves to.
func (c *Config) Path() string { return c.path }

// Save writes the fleet back to disk with 0600 permissions.
//
// The write is atomic — a temp file in the same directory, then a
// rename — so an interrupted save cannot leave a truncated fleet
// behind. On Windows the mode is advisory: NTFS uses ACLs and Go's
// Chmod cannot express "owner only", so the file inherits the
// directory's ACL instead.
func (c *Config) Save() error {
	if c.path == "" {
		c.path = DefaultPath()
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(c.path), err)
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	// Write to a temp file in the same directory and rename, so an
	// interrupted save cannot leave a truncated fleet behind.
	tmp, err := os.CreateTemp(filepath.Dir(c.path), ".config-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), c.path)
}

// Add puts a server in the fleet. It becomes the default when it is
// the first one, or when makeDefault is set.
func (c *Config) Add(name string, s *Server, makeDefault bool) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	if _, exists := c.Servers[name]; exists {
		return fmt.Errorf("%w: %s", ErrExists, name)
	}
	if err := s.Validate(); err != nil {
		return err
	}
	s.Name = name
	if c.Servers == nil {
		c.Servers = map[string]*Server{}
	}
	c.Servers[name] = s
	if makeDefault || len(c.Servers) == 1 {
		c.Default = name
	}
	return nil
}

// Remove drops a server from the fleet, moving the default if needed.
func (c *Config) Remove(name string) error {
	if _, ok := c.Servers[name]; !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	delete(c.Servers, name)
	if c.Default == name {
		c.Default = ""
	}
	// With exactly one server left the default is unambiguous, so adopt
	// it — the same rule Add applies to the first server. With several
	// left, an empty default stays empty rather than picking for the
	// user.
	if c.Default == "" && len(c.Servers) == 1 {
		for n := range c.Servers {
			c.Default = n
		}
	}
	return nil
}

// SetDefault marks a server as the default.
func (c *Config) SetDefault(name string) error {
	if _, ok := c.Servers[name]; !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	c.Default = name
	return nil
}

// Names returns the fleet's server names, sorted.
func (c *Config) Names() []string {
	out := make([]string, 0, len(c.Servers))
	for n := range c.Servers {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// List returns every server, sorted by name, plus the environment
// server when the environment supplies credentials.
func (c *Config) List() []*Server {
	out := make([]*Server, 0, len(c.Servers)+1)
	if env := ServerFromEnv(); env != nil {
		out = append(out, env)
	}
	for _, n := range c.Names() {
		out = append(out, c.Servers[n])
	}
	return out
}

// Filter returns the servers matching every given tag. With no tags it
// returns the whole fleet.
func (c *Config) Filter(tags []string) []*Server {
	all := c.List()
	if len(tags) == 0 {
		return all
	}
	var out []*Server
	for _, s := range all {
		match := true
		for _, t := range tags {
			if !s.HasTag(t) {
				match = false
				break
			}
		}
		if match {
			out = append(out, s)
		}
	}
	return out
}

// ServerFromEnv builds a server from the environment, or returns nil when
// the environment does not carry credentials.
//
// The key may be given as a bare key alongside BWG_VEID, or as a
// combined "veid:api_key" — the pairing the billing portal's CSV
// export uses, and the only form that needs a single variable.
func ServerFromEnv() *Server {
	key := firstNonEmpty(os.Getenv(EnvAPIKey), os.Getenv(EnvAPIKeyLegacy))
	veid := strings.TrimSpace(os.Getenv(EnvVEID))

	if v, k, ok := strings.Cut(key, ":"); ok {
		// Only treat it as combined when the left side looks like a VEID.
		if _, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			if veid == "" {
				veid = strings.TrimSpace(v)
			}
			key = strings.TrimSpace(k)
		}
	}
	if key == "" || veid == "" {
		return nil
	}
	return &Server{
		Name: EnvServerName, VEID: veid, APIKey: key, FromEnv: true,
		Note: "from the environment",
	}
}

// APIKeyEnvVar names the variable the key would come from, so that
// error messages and `bwg server show env` point at the one actually
// in play rather than at whichever spelling the docs happen to use.
func APIKeyEnvVar() string {
	if LegacyAPIKeyEnv() {
		return EnvAPIKeyLegacy
	}
	return EnvAPIKey
}

// LegacyAPIKeyEnv reports whether the deprecated spelling is the only
// one set. Both set is not a warning: that is what migrating looks
// like, and the current name wins.
func LegacyAPIKeyEnv() bool {
	return strings.TrimSpace(os.Getenv(EnvAPIKey)) == "" &&
		strings.TrimSpace(os.Getenv(EnvAPIKeyLegacy)) != ""
}

// EnvReadOnlyRequested reports whether the environment forces
// read-only mode.
func EnvReadOnlyRequested() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvReadOnly))) {
	case "", "0", "false", "no", "off":
		return false
	}
	return true
}

// Resolve picks the server a command should act on.
//
// Most explicit wins: a flag beats an environment variable, which
// beats a stored default. Environment credentials outrank the stored
// default because someone set them in this shell, for this run.
//
//  1. name, when given (--server)
//  2. $BWG_SERVER
//  3. credentials in the environment
//  4. the configured default
//  5. the only configured server
func (c *Config) Resolve(name string) (*Server, error) {
	lookup := func(n string) (*Server, error) {
		if n == EnvServerName {
			if env := ServerFromEnv(); env != nil {
				return env, nil
			}
			return nil, fmt.Errorf("%w: %s (set %s and %s to define it)",
				ErrNotFound, EnvServerName, EnvVEID, EnvAPIKey)
		}
		s, ok := c.Servers[n]
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, n)
		}
		return s, nil
	}

	if name != "" {
		return lookup(name)
	}
	if n := strings.TrimSpace(os.Getenv(EnvServer)); n != "" {
		return lookup(n)
	}
	if env := ServerFromEnv(); env != nil {
		return env, nil
	}
	if c.Default != "" {
		return lookup(c.Default)
	}
	switch len(c.Servers) {
	case 0:
		return nil, ErrNoServers
	case 1:
		for _, s := range c.Servers {
			return s, nil
		}
	}
	return nil, fmt.Errorf("%w (have: %s)", ErrAmbiguous, strings.Join(c.Names(), ", "))
}

var nameRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

// ValidateName checks that a server name is usable as a CLI argument.
func ValidateName(name string) error {
	if name == "" {
		return errors.New("server name cannot be empty")
	}
	if name == EnvServerName {
		return fmt.Errorf("%q is reserved for credentials from the environment", EnvServerName)
	}
	if !nameRE.MatchString(name) {
		return fmt.Errorf("server name %q must be alphanumeric, with . _ - allowed after the first character", name)
	}
	return nil
}

// -- billing portal CSV import ------------------------------------------

// ImportedServer is one row of a billing-portal API key export.
type ImportedServer struct {
	Name   string
	Server *Server
}

// ParseCSV reads a BandwagonHost billing-portal API key export.
//
// The portal's column names have changed over the years, so columns
// are matched by fuzzy header name rather than position, and a
// header-less file is accepted when the columns are unambiguous.
func ParseCSV(r io.Reader) ([]ImportedServer, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	cr.TrimLeadingSpace = true

	rows, err := cr.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("reading CSV: %w", err)
	}
	if len(rows) == 0 {
		return nil, errors.New("the CSV is empty")
	}

	veidCol, keyCol, nameCol := -1, -1, -1
	start := 0
	if header := rows[0]; looksLikeHeader(header) {
		start = 1
		for i, h := range header {
			switch norm := normalizeHeader(h); {
			case norm == "veid" || norm == "vpsid" || norm == "id" || norm == "serverid":
				veidCol = i
			case strings.Contains(norm, "apikey") || norm == "key":
				keyCol = i
			case norm == "hostname" || norm == "name" || norm == "label" || norm == "service":
				if nameCol == -1 {
					nameCol = i
				}
			}
		}
	}
	// No usable header: fall back to detecting the columns by content.
	if veidCol == -1 || keyCol == -1 {
		v, k := detectColumns(rows[start:])
		if v == -1 || k == -1 {
			return nil, errors.New(
				"could not find veid and api_key columns — expected a header naming them, " +
					"or a row with a numeric VEID and a private_... key")
		}
		veidCol, keyCol = v, k
	}

	var out []ImportedServer
	used := map[string]bool{}
	for _, row := range rows[start:] {
		if len(row) <= veidCol || len(row) <= keyCol {
			continue
		}
		veid := strings.TrimSpace(row[veidCol])
		key := strings.TrimSpace(row[keyCol])
		if veid == "" || key == "" {
			continue
		}
		s := &Server{VEID: veid, APIKey: key}
		if s.Validate() != nil {
			continue
		}

		name := ""
		if nameCol >= 0 && nameCol < len(row) {
			name = sanitizeName(row[nameCol])
		}
		if name == "" {
			name = "vps-" + veid
		}
		// Hostnames repeat across a fleet more often than you would hope.
		if used[name] {
			for n := 2; ; n++ {
				candidate := fmt.Sprintf("%s-%d", name, n)
				if !used[candidate] {
					name = candidate
					break
				}
			}
		}
		used[name] = true

		s.Name = name
		out = append(out, ImportedServer{Name: name, Server: s})
	}
	if len(out) == 0 {
		return nil, errors.New("no valid veid/api_key pairs found in the CSV")
	}
	return out, nil
}

func looksLikeHeader(row []string) bool {
	for _, cell := range row {
		n := normalizeHeader(cell)
		if n == "veid" || strings.Contains(n, "apikey") || n == "vpsid" {
			return true
		}
	}
	return false
}

func normalizeHeader(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// detectColumns finds the veid and api_key columns by what the values
// look like: an all-numeric column and a column of key-shaped strings.
func detectColumns(rows [][]string) (veid, key int) {
	veid, key = -1, -1
	for _, row := range rows {
		for i, cell := range row {
			cell = strings.TrimSpace(cell)
			if cell == "" {
				continue
			}
			if key == -1 && len(cell) >= 16 && !strings.ContainsAny(cell, " \t") &&
				(strings.HasPrefix(cell, "private_") || strings.Count(cell, "_") == 1) {
				key = i
				continue
			}
			if veid == -1 && len(cell) >= 4 && len(cell) <= 12 {
				if _, err := strconv.Atoi(cell); err == nil {
					veid = i
				}
			}
		}
		if veid >= 0 && key >= 0 {
			return veid, key
		}
	}
	return veid, key
}

var unsafeName = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func sanitizeName(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	s = unsafeName.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-._")
	if s != "" && ValidateName(s) != nil {
		return ""
	}
	return s
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}
