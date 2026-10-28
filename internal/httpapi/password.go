package httpapi

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const passwordIterations = 600000

type passwordRecord struct {
	Version int    `json:"version"`
	Salt    []byte `json:"salt"`
	Hash    []byte `json:"hash"`
}

func readPassword(path string) (*passwordRecord, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 4096 {
		return nil, errors.New("invalid password file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var record passwordRecord
	if err := json.Unmarshal(data, &record); err != nil || record.Version != 1 || len(record.Salt) != 32 || len(record.Hash) != 32 {
		return nil, errors.New("invalid password file; restore auth.json from a trusted backup")
	}
	if err := os.Chmod(path, 0600); err != nil {
		return nil, err
	}
	return &record, nil
}

func passwordHash(password string, salt []byte) ([]byte, error) {
	return pbkdf2.Key(sha256.New, password, salt, passwordIterations, 32)
}

func savePassword(path, password string) (*passwordRecord, error) {
	record := &passwordRecord{Version: 1, Salt: make([]byte, 32)}
	if _, err := rand.Read(record.Salt); err != nil {
		return nil, err
	}
	var err error
	record.Hash, err = passwordHash(password, record.Salt)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".auth-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return nil, err
	}
	if err := file.Sync(); err != nil {
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	// Publish the complete file without overwriting another initializer's password.
	if err := os.Link(file.Name(), path); err != nil {
		return nil, fmt.Errorf("save password: %w", err)
	}
	return record, nil
}
