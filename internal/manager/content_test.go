package manager

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigurationContentAndExportThroughSameUserSocket(t *testing.T) {
	path, server := startTestAPIServer(t)
	m := server.owner
	m.configDir = t.TempDir()
	m.maxConfigBytes = 4096
	m.externalConfigs = make(map[string]externalConfiguration)
	externalPath := filepath.Join(m.configDir, "physical.kbd")
	content := "(defcfg input (device-file \"/dev/null\"))\n(defsrc a)\n"
	if err := os.WriteFile(externalPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	if err := m.openManagedConfigurationStore(base); err != nil {
		t.Fatal(err)
	}
	m.refreshExternalConfigurationRegistry()
	externalID := opaqueExternalConfigurationID(externalPath)
	managedID, err := newConfigurationID()
	if err != nil {
		t.Fatal(err)
	}
	managed := managedConfiguration{Version: managedConfigurationStoreVersion, Ownership: ConfigurationManaged, ID: managedID,
		Name: "Managed", Model: ManagedConfigurationModel{DeviceID: "dev_one", Behavior: "(defsrc a)"},
		Revision: 1, ContentRevision: 1, Enabled: false, Digest: configurationDigest([]byte(content))}
	if err := m.storeManagedConfiguration(managed, []byte(content)); err != nil {
		t.Fatal(err)
	}
	reader, connection := dialAPI(t, path)
	writeAPIRequest(t, connection, `{"type":"request","id":"hello","method":"session.hello","params":{"supported_versions":[1]}}`)
	_ = readAPIResponse(t, reader)
	request := func(id, method string, params any) apiResponse {
		t.Helper()
		encoded, err := json.Marshal(params)
		if err != nil {
			t.Fatal(err)
		}
		writeAPIRequest(t, connection, `{"type":"request","id":"`+id+`","method":"`+method+`","params":`+string(encoded)+`}`)
		return readAPIResponse(t, reader)
	}
	read := request("read", "configuration.content.get", configurationContentParams{ConfigurationID: externalID, ExpectedRevision: externalContentRevision([]byte(content))})
	if read.Error != nil {
		t.Fatal(read.Error)
	}
	got := read.Result.(map[string]any)
	if got["content"] != content || got["digest"] != "sha256:"+configurationDigest([]byte(content)) || got["content_revision"] != float64(externalContentRevision([]byte(content))) || got["ownership"] != string(ConfigurationExternal) || strings.Contains(strings.Join([]string{got["configuration_id"].(string), got["digest"].(string)}, ""), m.configDir) {
		t.Fatalf("unsafe external read: %#v", got)
	}
	if rejected := request("managed-read", "configuration.content.get", configurationContentParams{ConfigurationID: managedID, ExpectedRevision: 1}); rejected.Error == nil || rejected.Error.Code != "unsupported_capability" {
		t.Fatalf("managed source leaked through read: %#v", rejected)
	}
	if rejected := request("external-export", "configuration.export", configurationExportParams{ConfigurationID: externalID, Format: managerRenderedKBDFormat}); rejected.Error == nil || rejected.Error.Code != "unsupported_capability" {
		t.Fatalf("external source was exported as managed: %#v", rejected)
	}
	if rejected := request("unknown-read", "configuration.content.get", configurationContentParams{ConfigurationID: "cfg_unknown", ExpectedRevision: 1}); rejected.Error == nil || rejected.Error.Code != "not_found" {
		t.Fatalf("unknown external ID was accepted: %#v", rejected)
	}
	if rejected := request("format", "configuration.export", configurationExportParams{ConfigurationID: managedID, Format: "portable"}); rejected.Error == nil || rejected.Error.Code != "unsupported_capability" {
		t.Fatalf("unknown format accepted: %#v", rejected)
	}
	for i := 0; i < 2; i++ {
		exported := request(fmt.Sprintf("export-%d", i), "configuration.export", configurationExportParams{ConfigurationID: managedID, Format: managerRenderedKBDFormat})
		if exported.Error != nil || exported.Result.(map[string]any)["content"] != content || exported.Result.(map[string]any)["digest"] != "sha256:"+managed.Digest || exported.Result.(map[string]any)["revision"] != float64(1) {
			t.Fatalf("export was not deterministic: %#v", exported)
		}
	}
	if err := writeAtomicPrivateFile(m.managedConfigurationPath(managed), []byte("(defsrc tampered)"), 0o400); err != nil {
		t.Fatal(err)
	}
	if changed := request("tampered-export", "configuration.export", configurationExportParams{ConfigurationID: managedID, Format: managerRenderedKBDFormat}); changed.Error == nil || changed.Error.Code != "configuration_changed" {
		t.Fatalf("tampered managed bytes were exported: %#v", changed)
	}
	if len(m.operations) != 0 || len(m.states) != 0 || len(m.managedConfigs) != 1 {
		t.Fatalf("content requests mutated mappings or operations: %#v %#v", m.operations, m.states)
	}
	if err := os.WriteFile(externalPath, []byte(content+"; revised\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if stale := request("stale", "configuration.content.get", configurationContentParams{ConfigurationID: externalID, ExpectedRevision: externalContentRevision([]byte(content))}); stale.Error == nil || stale.Error.Code != "stale_revision" {
		t.Fatalf("stale content was returned: %#v", stale)
	}
	if refreshed := request("fresh", "configuration.content.get", configurationContentParams{ConfigurationID: externalID, ExpectedRevision: externalContentRevision([]byte(content + "; revised\n"))}); refreshed.Error != nil || refreshed.Result.(map[string]any)["content_revision"] != float64(externalContentRevision([]byte(content+"; revised\n"))) {
		t.Fatalf("new external revision unavailable: %#v", refreshed)
	}
	if err := os.WriteFile(externalPath, []byte(strings.Repeat("a", int(m.maxConfigBytes)+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if oversized := request("oversized", "configuration.content.get", configurationContentParams{ConfigurationID: externalID, ExpectedRevision: externalContentRevision([]byte(content + "; revised\n"))}); oversized.Error == nil || oversized.Error.Code != "resource_exhausted" {
		t.Fatalf("oversized external content was not rejected: %#v", oversized)
	}
	if missing := request("missing", "configuration.export", configurationExportParams{ConfigurationID: "cfg_unknown", Format: managerRenderedKBDFormat}); missing.Error == nil || missing.Error.Code != "not_found" {
		t.Fatalf("unknown export ID was accepted: %#v", missing)
	}
}

func TestExternalContentDetectsChangeDuringRead(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "keyboard.kbd")
	content := []byte("(defsrc a)\n")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	m := &manager{configDir: directory, maxConfigBytes: 4096, externalConfigs: make(map[string]externalConfiguration)}
	m.refreshExternalConfigurationRegistry()
	previous := externalContentReader
	defer func() { externalContentReader = previous }()
	externalContentReader = func(string, int64) ([]byte, error) { return []byte("(defsrc b)\n"), nil }
	result := m.externalConfigurationContent(configurationContentParams{
		ConfigurationID: opaqueExternalConfigurationID(path), ExpectedRevision: externalContentRevision(content),
	})
	if result.err == nil || result.err.Code != "configuration_changed" {
		t.Fatalf("changed content was returned: %#v", result)
	}
}

func TestExternalContentRejectsSymlinksAndOversizedBytes(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "source")
	if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "linked.kbd")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readBoundedExternalContent(link, 20); err == nil {
		t.Fatal("symlink content was read")
	}
	if _, err := readBoundedExternalContent(target, 2); err == nil {
		t.Fatal("oversized content was read")
	}
}

func TestExternalContentRevisionSurvivesRemovalAndManagerRestart(t *testing.T) {
	base := t.TempDir()
	directory := t.TempDir()
	path := filepath.Join(directory, "keyboard.kbd")
	first := []byte("(defsrc a)\n")
	second := []byte("(defsrc b)\n")
	if err := os.WriteFile(path, first, 0o600); err != nil {
		t.Fatal(err)
	}
	m := &manager{configDir: directory, maxConfigBytes: 4096, externalConfigs: make(map[string]externalConfiguration)}
	if err := m.openManagedConfigurationStore(base); err != nil {
		t.Fatal(err)
	}
	m.refreshExternalConfigurationRegistry()
	id := opaqueExternalConfigurationID(path)
	old := m.externalConfigs[id].ContentRevision
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	m.refreshExternalConfigurationRegistry()
	if err := os.WriteFile(path, second, 0o600); err != nil {
		t.Fatal(err)
	}
	restarted := &manager{configDir: directory, maxConfigBytes: 4096, externalConfigs: make(map[string]externalConfiguration)}
	if err := restarted.openManagedConfigurationStore(base); err != nil {
		t.Fatal(err)
	}
	restarted.loadExternalConfigurationRegistry()
	if stale := restarted.externalConfigurationContent(configurationContentParams{ConfigurationID: id, ExpectedRevision: old}); stale.err == nil || stale.err.Code != "stale_revision" {
		t.Fatalf("recreated file reused a stale revision: %#v", stale)
	}
	if same := externalContentRevision(first); same != old || same == externalContentRevision(second) {
		t.Fatalf("content revision is not stable across restart: %d %d", same, old)
	}
}

func TestConfigurationContentJSONLinesFixture(t *testing.T) {
	file, err := os.Open(filepath.Join("..", "..", "tests", "fixtures", "configuration-content-v1.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	var requests []apiRequest
	var responses []apiResponse
	for scanner.Scan() {
		var kind struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &kind); err != nil {
			t.Fatal(err)
		}
		if kind.Type == "request" {
			var request apiRequest
			if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
				t.Fatal(err)
			}
			requests = append(requests, request)
		} else {
			var response apiResponse
			if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			responses = append(responses, response)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 || len(responses) != 2 || requests[0].Method != "configuration.content.get" || requests[1].Method != "configuration.export" {
		t.Fatalf("unexpected content fixture: %#v %#v", requests, responses)
	}
	for index, response := range responses {
		if response.ID != requests[index].ID || response.Error != nil {
			t.Fatalf("unpaired fixture response: %#v", response)
		}
		result := response.Result.(map[string]any)
		content := result["content"].(string)
		if result["digest"] != "sha256:"+configurationDigest([]byte(content)) {
			t.Fatalf("fixture content digest mismatch: %#v", result)
		}
		if index == 0 && result["content_revision"] != float64(externalContentRevision([]byte(content))) {
			t.Fatalf("fixture revision mismatch: %#v", result)
		}
	}
}
