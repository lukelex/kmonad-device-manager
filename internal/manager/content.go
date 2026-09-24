package manager

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"unicode/utf8"
)

const managerRenderedKBDFormat = "manager_rendered_kbd"

var externalContentReader = readBoundedExternalContent

type configurationContentParams struct {
	ConfigurationID  string `json:"configuration_id"`
	ExpectedRevision uint64 `json:"expected_revision"`
}

type configurationExportParams struct {
	ConfigurationID string `json:"configuration_id"`
	Format          string `json:"format"`
}

type configurationContentResult struct {
	ConfigurationID string                 `json:"configuration_id"`
	Ownership       ConfigurationOwnership `json:"ownership"`
	ContentRevision uint64                 `json:"content_revision"`
	Digest          string                 `json:"digest"`
	Content         string                 `json:"content"`
}

type configurationExportResult struct {
	ConfigurationID string `json:"configuration_id"`
	Revision        uint64 `json:"revision"`
	Format          string `json:"format"`
	Digest          string `json:"digest"`
	Content         string `json:"content"`
}

func (m *manager) externalConfigurationContent(params configurationContentParams) commandResult {
	if params.ConfigurationID == "" || params.ExpectedRevision == 0 {
		return commandResult{err: &apiError{Code: "invalid_request", Message: "configuration_id and positive expected_revision are required"}}
	}
	previous, previouslyKnown := m.externalConfigs[params.ConfigurationID]
	m.refreshExternalConfigurationRegistry()
	if _, managed := m.managedConfigs[params.ConfigurationID]; managed {
		return commandResult{err: &apiError{Code: "unsupported_capability", Message: "managed source is not available through external content read; use configuration.export"}}
	}
	entry, found := m.externalConfigs[params.ConfigurationID]
	if !found && previouslyKnown {
		if info, err := os.Lstat(previous.Path); err == nil && info.Size() > m.configurationByteLimit() {
			return commandResult{err: &apiError{Code: "resource_exhausted", Message: "external configuration exceeds the content limit"}}
		}
		return commandResult{err: &apiError{Code: "configuration_changed", Message: "external configuration is no longer readable"}}
	}
	if !found || entry.Ownership != ConfigurationExternal || entry.Path != filepath.Join(m.configDir, entry.Name) || filepath.Base(entry.Name) != entry.Name {
		return commandResult{err: &apiError{Code: "not_found", Message: "external configuration does not exist"}}
	}
	if entry.ContentRevision != params.ExpectedRevision {
		return commandResult{err: &apiError{Code: "stale_revision", Message: "external configuration content changed; refresh the inventory"}}
	}
	content, err := externalContentReader(entry.Path, m.configurationByteLimit())
	if err != nil {
		return commandResult{err: &apiError{Code: "configuration_changed", Message: "external configuration is unavailable, unsafe, or exceeds the content limit"}}
	}
	if !utf8.Valid(content) || configurationDigest(content) != entry.Signature {
		return commandResult{err: &apiError{Code: "configuration_changed", Message: "external configuration changed during reading or is not UTF-8 text"}}
	}
	return boundedContentResult(configurationContentResult{ConfigurationID: entry.ID, Ownership: ConfigurationExternal,
		ContentRevision: entry.ContentRevision, Digest: "sha256:" + entry.Signature, Content: string(content)})
}

func (m *manager) exportManagedConfiguration(params configurationExportParams) commandResult {
	if params.ConfigurationID == "" || params.Format == "" {
		return commandResult{err: &apiError{Code: "invalid_request", Message: "configuration_id and format are required"}}
	}
	if params.Format != managerRenderedKBDFormat {
		return commandResult{err: &apiError{Code: "unsupported_capability", Message: "unsupported configuration export format"}}
	}
	entry, found := m.managedConfigs[params.ConfigurationID]
	if !found {
		if _, external := m.externalConfigs[params.ConfigurationID]; external {
			return commandResult{err: &apiError{Code: "unsupported_capability", Message: "external configurations are not managed exports"}}
		}
		return commandResult{err: &apiError{Code: "not_found", Message: "managed configuration does not exist"}}
	}
	content, err := readFileLimited(m.managedConfigurationPath(entry), m.configurationByteLimit())
	if err != nil || !utf8.Valid(content) || configurationDigest(content) != entry.Digest {
		return commandResult{err: &apiError{Code: "configuration_changed", Message: "manager-owned revision changed or cannot be exported"}}
	}
	return boundedContentResult(configurationExportResult{ConfigurationID: entry.ID, Revision: entry.Revision,
		Format: params.Format, Digest: "sha256:" + entry.Digest, Content: string(content)})
}

// Lstat both sides of the descriptor read to reject symlink substitution and
// replacement while bytes are being read. No host path crosses the API.
func readBoundedExternalContent(path string, limit int64) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() {
		return nil, os.ErrInvalid
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) || opened.Size() > limit {
		return nil, os.ErrInvalid
	}
	content, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(content)) > limit {
		return nil, os.ErrInvalid
	}
	after, err := os.Lstat(path)
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(opened, after) || !after.ModTime().Equal(opened.ModTime()) || after.Size() != opened.Size() {
		return nil, os.ErrInvalid
	}
	return content, nil
}

func boundedContentResult(result any) commandResult {
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded)+128 > apiFrameLimit {
		return commandResult{err: &apiError{Code: "resource_exhausted", Message: "configuration content exceeds the API response limit"}}
	}
	return commandResult{result: result}
}
