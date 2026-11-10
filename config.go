package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	ClientID     string
	ClientSecret string
	ItemIDs      []string
}

// projectRoot localiza a raiz do projeto a partir do executável (não do cwd),
// subindo de bin/ quando for o caso.
func projectRoot() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(exe)
	if strings.EqualFold(filepath.Base(dir), "bin") {
		dir = filepath.Dir(dir)
	}
	return dir, nil
}

func loadConfig() (*Config, error) {
	root, err := projectRoot()
	if err != nil {
		return nil, fmt.Errorf("não foi possível localizar o executável: %w", err)
	}
	path := filepath.Join(root, ".env")
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("arquivo .env não encontrado em %s (copie .env.example para .env e preencha)", path)
		}
		return nil, err
	}
	env, err := godotenv.Read(path)
	if err != nil {
		return nil, fmt.Errorf("erro ao ler %s: %w", path, err)
	}

	cfg := &Config{
		ClientID:     strings.TrimSpace(env["PLUGGY_CLIENT_ID"]),
		ClientSecret: strings.TrimSpace(env["PLUGGY_CLIENT_SECRET"]),
	}
	for _, id := range strings.Split(env["PLUGGY_ITEM_IDS"], ",") {
		if id = strings.TrimSpace(id); id != "" {
			cfg.ItemIDs = append(cfg.ItemIDs, id)
		}
	}

	var missing []string
	if cfg.ClientID == "" {
		missing = append(missing, "PLUGGY_CLIENT_ID")
	}
	if cfg.ClientSecret == "" {
		missing = append(missing, "PLUGGY_CLIENT_SECRET")
	}
	if len(cfg.ItemIDs) == 0 {
		missing = append(missing, "PLUGGY_ITEM_IDS")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("variáveis ausentes ou vazias em %s: %s", path, strings.Join(missing, ", "))
	}
	return cfg, nil
}
