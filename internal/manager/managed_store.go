package manager

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const managedConfigurationStoreVersion = 1

type managedConfiguration struct {
	Version  int                       `json:"version"`
	ID       string                    `json:"id"`
	Name     string                    `json:"name"`
	Model    ManagedConfigurationModel `json:"model"`
	Revision uint64                    `json:"revision"`
	// ContentRevision names the immutable rendered KMonad revision. Desired
	// lifecycle changes increment Revision without rewriting candidate bytes.
	ContentRevision uint64 `json:"content_revision"`
	Digest          string `json:"digest"`
	Enabled         bool   `json:"enabled"`
}

func (m *manager) openManagedConfigurationStore(base string) error {
	if base == "" {
		return fmt.Errorf("manager state directory is empty")
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(base, 0o700); err != nil {
		return err
	}
	m.managedConfigDir = filepath.Join(base, "configurations")
	if err := os.MkdirAll(m.managedConfigDir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(m.managedConfigDir, 0o700); err != nil {
		return err
	}
	if m.managedConfigs == nil {
		m.managedConfigs = make(map[string]managedConfiguration)
	}
	entries, err := os.ReadDir(m.managedConfigDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := readFileLimited(filepath.Join(m.managedConfigDir, entry.Name()), defaultMaxConfigBytes)
		if err != nil {
			continue
		}
		var configuration managedConfiguration
		if json.Unmarshal(data, &configuration) != nil {
			continue
		}
		configuration = normalizedManagedConfiguration(configuration)
		if !validManagedConfiguration(configuration) {
			continue
		}
		content, err := readFileLimited(m.managedConfigurationPath(configuration), defaultMaxConfigBytes)
		if err != nil || configurationDigest(content) != configuration.Digest {
			continue
		}
		m.managedConfigs[configuration.ID] = configuration
	}
	return nil
}

func (m *manager) managedConfigurationPath(configuration managedConfiguration) string {
	configuration = normalizedManagedConfiguration(configuration)
	return filepath.Join(m.managedConfigDir, configuration.ID, fmt.Sprintf("%020d.kbd", configuration.ContentRevision))
}

func (m *manager) managedConfigurationMetadataPath(id string) string {
	return filepath.Join(m.managedConfigDir, id+".json")
}

func (m *manager) storeManagedConfiguration(configuration managedConfiguration, content []byte) error {
	configuration = normalizedManagedConfiguration(configuration)
	if !validManagedConfiguration(configuration) || m.managedConfigDir == "" {
		return fmt.Errorf("managed configuration storage is unavailable")
	}
	if configurationDigest(content) != configuration.Digest {
		return fmt.Errorf("managed configuration content does not match its digest")
	}
	directory := filepath.Dir(m.managedConfigurationPath(configuration))
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return err
	}
	if err := writeAtomicPrivateFile(m.managedConfigurationPath(configuration), content, 0o400); err != nil {
		return err
	}
	if err := m.storeManagedConfigurationMetadata(configuration); err != nil {
		return err
	}
	if m.managedConfigs == nil {
		m.managedConfigs = make(map[string]managedConfiguration)
	}
	m.managedConfigs[configuration.ID] = configuration
	return nil
}

func (m *manager) storeManagedConfigurationMetadata(configuration managedConfiguration) error {
	configuration = normalizedManagedConfiguration(configuration)
	if !validManagedConfiguration(configuration) || m.managedConfigDir == "" {
		return fmt.Errorf("managed configuration storage is unavailable")
	}
	data, err := json.Marshal(configuration)
	if err != nil {
		return err
	}
	if err := writeAtomicPrivateFile(m.managedConfigurationMetadataPath(configuration.ID), append(data, '\n'), 0o600); err != nil {
		return err
	}
	if m.managedConfigs == nil {
		m.managedConfigs = make(map[string]managedConfiguration)
	}
	m.managedConfigs[configuration.ID] = configuration
	return nil
}

func (m *manager) removeManagedConfigurationMetadata(id string) error {
	if !validConfigurationID(id) || m.managedConfigDir == "" {
		return fmt.Errorf("managed configuration storage is unavailable")
	}
	if err := os.Remove(m.managedConfigurationMetadataPath(id)); err != nil && !os.IsNotExist(err) {
		return err
	}
	delete(m.managedConfigs, id)
	return syncDirectory(m.managedConfigDir)
}

func writeAtomicPrivateFile(path string, data []byte, mode os.FileMode) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".kmonad-device-manager-write-")
	if err != nil {
		return err
	}
	temporary := file.Name()
	remove := true
	defer func() {
		_ = file.Close()
		if remove {
			_ = os.Remove(temporary)
		}
	}()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Chmod(mode); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		return err
	}
	remove = false
	return syncDirectory(filepath.Dir(path))
}

func (m *manager) configurationPaths() ([]string, error) {
	paths := make([]string, 0)
	if m.configDir != "" {
		entries, err := os.ReadDir(m.configDir)
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".kbd") {
				paths = append(paths, filepath.Join(m.configDir, entry.Name()))
			}
		}
	}
	for _, configuration := range m.managedConfigs {
		if configuration.Enabled {
			paths = append(paths, m.managedConfigurationPath(configuration))
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func validManagedConfiguration(configuration managedConfiguration) bool {
	return configuration.Version == managedConfigurationStoreVersion && validConfigurationID(configuration.ID) &&
		strings.TrimSpace(configuration.Name) != "" && configuration.Revision > 0 && configuration.ContentRevision > 0 && configuration.Digest != "" &&
		configuration.Model.DeviceID != "" && !containsInputConfiguration(configuration.Model.Behavior)
}

func normalizedManagedConfiguration(configuration managedConfiguration) managedConfiguration {
	if configuration.ContentRevision == 0 {
		configuration.ContentRevision = configuration.Revision
	}
	return configuration
}

func validConfigurationID(id string) bool {
	if len(id) != 36 || !strings.HasPrefix(id, "cfg_") {
		return false
	}
	_, err := hex.DecodeString(id[4:])
	return err == nil
}

func newConfigurationID() (string, error) {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return "cfg_" + hex.EncodeToString(data), nil
}

func configurationDigest(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}
