package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type Profile struct {
	URL    string `json:"url"`
	Token  string `json:"token,omitempty"`
	CACert string `json:"caCert,omitempty"`
}
type Config struct {
	Current  string             `json:"current,omitempty"`
	Profiles map[string]Profile `json:"profiles"`
}

var profileName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}$`)

func defaultConfigPath(env func(string) string) (string, error) {
	if v := env("ZELA_CONFIG"); v != "" {
		return v, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "zela", "config.json"), nil
}
func loadConfig(path string) (Config, error) {
	cfg := Config{Profiles: map[string]Profile{}}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("open CLI configuration: %w", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return cfg, err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
		return cfg, fmt.Errorf("CLI configuration must be a regular private file; run chmod 600 on it")
	}
	b, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil {
		return cfg, err
	}
	if len(b) > 1<<20 {
		return cfg, fmt.Errorf("CLI configuration too large")
	}
	if json.Unmarshal(b, &cfg) != nil {
		return cfg, fmt.Errorf("invalid CLI configuration JSON")
	}
	if cfg.Profiles == nil {
		cfg.Profiles = map[string]Profile{}
	}
	return cfg, nil
}
func saveConfig(path string, cfg Config) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if st, err := os.Stat(dir); err != nil {
		return err
	} else if st.Mode().Perm()&0022 != 0 {
		return fmt.Errorf("configuration directory %s is writable by other users; run chmod 700 on it", dir)
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	f, err := os.CreateTemp(dir, ".zela-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(name, path)
}
func readInputFile(path string, in io.Reader, private bool) ([]byte, error) {
	var r io.Reader = in
	if path != "-" {
		f, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("open input file: %w", err)
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil {
			return nil, err
		}
		if !st.Mode().IsRegular() {
			return nil, fmt.Errorf("input must be a regular file")
		}
		if private && st.Mode().Perm()&0077 != 0 {
			return nil, fmt.Errorf("secret input file must be private; run chmod 600 on it")
		}
		r = f
	}
	b, err := io.ReadAll(io.LimitReader(r, (1<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(b) > 1<<20 {
		return nil, fmt.Errorf("input file exceeds 1 MiB")
	}
	return b, nil
}
func readSecret(path string, in io.Reader) (string, error) {
	b, err := readInputFile(path, in, true)
	if err != nil {
		return "", err
	}
	s := strings.TrimSpace(string(b))
	if s == "" || len(s) > 4096 || strings.ContainsAny(s, "\r\n\x00") {
		return "", fmt.Errorf("secret must contain a single nonempty line")
	}
	return s, nil
}
