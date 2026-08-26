package install

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type Decision string

const (
	DecisionFresh     Decision = "fresh"
	DecisionSame      Decision = "same"
	DecisionUpgrade   Decision = "upgrade"
	DecisionDowngrade Decision = "downgrade"
	DecisionUntrusted Decision = "untrusted"
)

const (
	RecordSchemaVersion  = "1"
	ProductName          = "codex-usage"
	SourceBuild          = "source_build"
	SourceTrustedRelease = "trusted_release"
)

type Candidate struct {
	Version        string
	ExecutablePath string
}

type Record struct {
	SchemaVersion    string `json:"schema_version"`
	Product          string `json:"product"`
	Version          string `json:"version"`
	Commit           string `json:"commit"`
	Dirty            bool   `json:"dirty"`
	BuildDate        string `json:"build_date"`
	OS               string `json:"os"`
	Arch             string `json:"arch"`
	ExecutablePath   string `json:"executable_path"`
	ExecutableSHA256 string `json:"executable_sha256"`
	Source           string `json:"source"`
	InstalledAt      string `json:"installed_at"`
}

func Load(path string) (*Record, error) {
	exists, err := validateRecordFile(path)
	if err != nil {
		return nil, fmt.Errorf("inspect install record %s: %w", path, err)
	}
	if !exists {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read install record %s: %w", path, err)
	}
	record, err := decodeRecord(data)
	if err != nil {
		return nil, fmt.Errorf("decode install record %s: %w", path, err)
	}
	if err := validateRecord(record); err != nil {
		return nil, fmt.Errorf("validate install record %s: %w", path, err)
	}
	return &record, nil
}

func Save(path string, record Record) error {
	if err := validateRecord(record); err != nil {
		return fmt.Errorf("validate install record %s: %w", path, err)
	}
	if _, err := validateRecordFile(path); err != nil {
		return fmt.Errorf("inspect install record destination %s: %w", path, err)
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("encode install record %s: %w", path, err)
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create install record directory %s: %w", dir, err)
	}
	temporary, err := os.CreateTemp(dir, ".install-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary install record beside %s: %w", path, err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("protect temporary install record %s: %w", temporaryPath, err)
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return fmt.Errorf("write temporary install record %s: %w", temporaryPath, err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync temporary install record %s: %w", temporaryPath, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary install record %s: %w", temporaryPath, err)
	}
	if err := replaceInstallRecord(temporaryPath, path); err != nil {
		return fmt.Errorf("replace install record %s: %w", path, err)
	}
	return nil
}

func Assess(recordPath, executablePath string, candidate Candidate) (Decision, error) {
	candidateVersion, err := parseVersion(candidate.Version)
	if err != nil {
		return "", fmt.Errorf("invalid candidate version %q: %w", candidate.Version, err)
	}
	if candidate.ExecutablePath == "" || !filepath.IsAbs(candidate.ExecutablePath) || filepath.Clean(candidate.ExecutablePath) != candidate.ExecutablePath {
		return "", fmt.Errorf("candidate executable must be an absolute clean path: %q", candidate.ExecutablePath)
	}
	candidateInfo, err := os.Lstat(candidate.ExecutablePath)
	if err != nil {
		return "", fmt.Errorf("inspect candidate executable %s: %w", candidate.ExecutablePath, err)
	}
	if !isSafeRegularFile(candidateInfo) {
		return "", fmt.Errorf("candidate executable is not a regular file: %s", candidate.ExecutablePath)
	}
	candidateDigest, err := FileSHA256(candidate.ExecutablePath)
	if err != nil {
		return "", err
	}
	record, err := Load(recordPath)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(executablePath)
	if errors.Is(err, os.ErrNotExist) {
		if record == nil {
			return DecisionFresh, nil
		}
		return DecisionUntrusted, nil
	}
	if err != nil {
		return "", fmt.Errorf("inspect installed executable %s: %w", executablePath, err)
	}
	if !isSafeRegularFile(info) || record == nil {
		return DecisionUntrusted, nil
	}
	if !samePath(record.ExecutablePath, executablePath) {
		return DecisionUntrusted, nil
	}
	installedDigest, err := FileSHA256(executablePath)
	if err != nil {
		return "", err
	}
	if record.ExecutableSHA256 != installedDigest {
		return DecisionUntrusted, nil
	}
	installedVersion, err := parseVersion(record.Version)
	if err != nil {
		return "", fmt.Errorf("invalid recorded version %q in %s: %w", record.Version, recordPath, err)
	}
	switch compareVersion(candidateVersion, installedVersion) {
	case -1:
		return DecisionDowngrade, nil
	case 1:
		return DecisionUpgrade, nil
	default:
		if candidateDigest != installedDigest {
			return DecisionUntrusted, nil
		}
		return DecisionSame, nil
	}
}

func FileSHA256(path string) (string, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("inspect executable %s for sha256: %w", path, err)
	}
	if !isSafeRegularFile(before) {
		return "", fmt.Errorf("refuse to hash non-regular executable %s", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open executable %s for sha256: %w", path, err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("inspect opened executable %s: %w", path, err)
	}
	if !isSafeRegularFile(opened) || !os.SameFile(before, opened) {
		return "", fmt.Errorf("executable changed or ceased to be regular while opening %s", path)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("hash executable %s: %w", path, err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

type numericVersion [3]uint64

func parseVersion(value string) (numericVersion, error) {
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return numericVersion{}, errors.New("version must contain exactly three numeric components")
	}
	var version numericVersion
	for index, part := range parts {
		if part == "" {
			return numericVersion{}, errors.New("version component is empty")
		}
		if len(part) > 1 && part[0] == '0' {
			return numericVersion{}, fmt.Errorf("version component %q has a leading zero", part)
		}
		for _, character := range part {
			if character < '0' || character > '9' {
				return numericVersion{}, fmt.Errorf("version component %q contains non-decimal characters", part)
			}
		}
		parsed, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return numericVersion{}, fmt.Errorf("version component %q is not an unsigned integer: %w", part, err)
		}
		version[index] = parsed
	}
	return version, nil
}

func compareVersion(left, right numericVersion) int {
	for index := range left {
		if left[index] < right[index] {
			return -1
		}
		if left[index] > right[index] {
			return 1
		}
	}
	return 0
}

func samePath(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func requireSameParentDirectory(temporaryPath, destinationPath string) error {
	temporaryParent, err := filepath.Abs(filepath.Dir(temporaryPath))
	if err != nil {
		return fmt.Errorf("resolve temporary install record parent %s: %w", temporaryPath, err)
	}
	destinationParent, err := filepath.Abs(filepath.Dir(destinationPath))
	if err != nil {
		return fmt.Errorf("resolve destination install record parent %s: %w", destinationPath, err)
	}
	if !samePath(temporaryParent, destinationParent) {
		return fmt.Errorf("install record replacement must remain in one directory: %s != %s", temporaryParent, destinationParent)
	}
	return nil
}

var recordFields = []string{
	"schema_version",
	"product",
	"version",
	"commit",
	"dirty",
	"build_date",
	"os",
	"arch",
	"executable_path",
	"executable_sha256",
	"source",
	"installed_at",
}

func decodeRecord(data []byte) (Record, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	opening, err := decoder.Token()
	if err != nil {
		return Record{}, err
	}
	if delimiter, ok := opening.(json.Delim); !ok || delimiter != '{' {
		return Record{}, errors.New("install record must be a JSON object")
	}
	allowed := make(map[string]struct{}, len(recordFields))
	for _, field := range recordFields {
		allowed[field] = struct{}{}
	}
	seen := make(map[string]struct{}, len(recordFields))
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return Record{}, err
		}
		field, ok := token.(string)
		if !ok {
			return Record{}, errors.New("install record field name must be a string")
		}
		if _, duplicate := seen[field]; duplicate {
			return Record{}, fmt.Errorf("duplicate install record field %q", field)
		}
		if _, known := allowed[field]; !known {
			return Record{}, fmt.Errorf("unknown install record field %q", field)
		}
		seen[field] = struct{}{}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return Record{}, fmt.Errorf("decode install record field %q: %w", field, err)
		}
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return Record{}, fmt.Errorf("install record field %q cannot be null", field)
		}
	}
	if _, err := decoder.Token(); err != nil {
		return Record{}, err
	}
	if token, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err != nil {
			return Record{}, err
		}
		return Record{}, fmt.Errorf("unexpected trailing JSON value %v", token)
	}
	for _, field := range recordFields {
		if _, present := seen[field]; !present {
			return Record{}, fmt.Errorf("missing install record field %q", field)
		}
	}

	strict := json.NewDecoder(bytes.NewReader(data))
	strict.DisallowUnknownFields()
	var record Record
	if err := strict.Decode(&record); err != nil {
		return Record{}, err
	}
	return record, nil
}

func validateRecord(record Record) error {
	if record.SchemaVersion != RecordSchemaVersion {
		return fmt.Errorf("schema_version=%q, want %q", record.SchemaVersion, RecordSchemaVersion)
	}
	if record.Product != ProductName {
		return fmt.Errorf("product=%q, want %q", record.Product, ProductName)
	}
	if _, err := parseVersion(record.Version); err != nil {
		return fmt.Errorf("invalid version %q: %w", record.Version, err)
	}
	if record.OS != "windows" && record.OS != "linux" {
		return fmt.Errorf("unsupported os %q", record.OS)
	}
	if record.OS != runtime.GOOS {
		return fmt.Errorf("record os %q does not match current host %q", record.OS, runtime.GOOS)
	}
	if record.Arch != "amd64" && record.Arch != "arm64" {
		return fmt.Errorf("unsupported arch %q", record.Arch)
	}
	if record.Arch != runtime.GOARCH {
		return fmt.Errorf("record arch %q does not match current host %q", record.Arch, runtime.GOARCH)
	}
	if record.ExecutablePath == "" || !filepath.IsAbs(record.ExecutablePath) || filepath.Clean(record.ExecutablePath) != record.ExecutablePath {
		return fmt.Errorf("executable_path must be an absolute clean path: %q", record.ExecutablePath)
	}
	if !validNonZeroLowerHex(record.ExecutableSHA256, sha256.Size*2) {
		return fmt.Errorf("invalid executable_sha256 %q", record.ExecutableSHA256)
	}
	installedAt, err := time.Parse(time.RFC3339, record.InstalledAt)
	if err != nil {
		return fmt.Errorf("invalid installed_at %q: %w", record.InstalledAt, err)
	}
	if installedAt.IsZero() {
		return errors.New("installed_at cannot be the zero time")
	}
	fullCommit := validNonZeroLowerHex(record.Commit, 40)
	switch record.Source {
	case SourceBuild:
		if record.Commit == "dev" {
			if !record.Dirty {
				return errors.New("source_build commit dev must be dirty")
			}
		} else if !fullCommit {
			return fmt.Errorf("source_build commit must be dev or a full lowercase sha: %q", record.Commit)
		}
	case SourceTrustedRelease:
		if record.Dirty {
			return errors.New("trusted_release cannot be dirty")
		}
		if !fullCommit {
			return fmt.Errorf("trusted_release commit must be a full lowercase sha: %q", record.Commit)
		}
	default:
		return fmt.Errorf("unsupported source %q", record.Source)
	}
	if record.BuildDate != "unknown" || record.Source != SourceBuild {
		buildDate, err := time.Parse(time.RFC3339, record.BuildDate)
		if err != nil {
			return fmt.Errorf("invalid build_date %q: %w", record.BuildDate, err)
		}
		if buildDate.IsZero() {
			return errors.New("build_date cannot be the zero time")
		}
	}
	return nil
}

func validLowerHex(value string, length int) bool {
	if len(value) != length || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validNonZeroLowerHex(value string, length int) bool {
	return validLowerHex(value, length) && strings.Trim(value, "0") != ""
}

func validateRecordFile(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !isSafeRegularFile(info) {
		return false, errors.New("install record path is not a regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o022 != 0 {
		return false, fmt.Errorf("install record is group/world writable: mode=%#o", info.Mode().Perm())
	}
	return true, nil
}
