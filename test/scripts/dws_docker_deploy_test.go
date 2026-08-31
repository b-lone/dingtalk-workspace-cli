package scripts_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testProfile = "ding-test-corp"
	testGroup   = "cid-test-group"
	testTitle   = "构建通知"
	testToken   = "test-http-token"
)

func TestComposeRequiresRegisteredDWSChannel(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker CLI is unavailable")
	}

	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("Abs(repo root) error = %v", err)
	}
	composePath := filepath.Join(repoRoot, "compose.yaml")
	stateDir := filepath.Join(t.TempDir(), "state")
	secretsDir := filepath.Join(t.TempDir(), "secrets")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(state) error = %v", err)
	}
	if err := os.MkdirAll(secretsDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(secrets) error = %v", err)
	}

	baseEnv := append(os.Environ(),
		"IMAGE_REF=registry.example.com/dws@sha256:"+strings.Repeat("a", 64),
		"DWS_STATE_DIR="+stateDir,
		"DWS_SECRETS_DIR="+secretsDir,
	)

	missing := exec.Command("docker", "compose", "-f", composePath, "config", "--format", "json")
	missing.Env = append(baseEnv, "DWS_CHANNEL=")
	missingOutput, missingErr := missing.CombinedOutput()
	if missingErr == nil {
		t.Fatalf("compose config without DWS_CHANNEL succeeded:\n%s", missingOutput)
	}

	present := exec.Command("docker", "compose", "-f", composePath, "config", "--format", "json")
	present.Env = append(baseEnv, "DWS_CHANNEL=test-registered-channel")
	presentOutput, err := present.CombinedOutput()
	if err != nil {
		t.Fatalf("compose config with DWS_CHANNEL error = %v\noutput:\n%s", err, presentOutput)
	}
	var rendered struct {
		Services map[string]struct {
			Environment map[string]string `json:"environment"`
		} `json:"services"`
	}
	if err := json.Unmarshal(presentOutput, &rendered); err != nil {
		t.Fatalf("Unmarshal(compose config) error = %v\noutput:\n%s", err, presentOutput)
	}
	if got := rendered.Services["dws"].Environment["DWS_CHANNEL"]; got != "test-registered-channel" {
		t.Fatalf("DWS_CHANNEL = %q, want test-registered-channel", got)
	}
}

func TestVerifyDWSHTTPContract(t *testing.T) {
	t.Parallel()

	server := newDWSHTTPServer(t, nil, "")
	tokenFile := writeTokenFile(t)
	verifyScript := repoScriptPath(t, "verify-dws-http.sh")

	cmd := exec.Command(
		"bash", verifyScript,
		"--base-url", server.URL,
		"--token-file", tokenFile,
		"--profile", testProfile,
		"--verify-group", testGroup,
		"--verify-title", testTitle,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("verify-dws-http.sh error = %v\noutput:\n%s", err, output)
	}
	for _, want := range []string{
		"DWS_VERIFIED_PROFILE=" + testProfile,
		"DWS_VERIFIED_GROUP_TITLE=" + testTitle,
	} {
		if !strings.Contains(string(output), want) {
			t.Fatalf("verification output missing %q:\n%s", want, output)
		}
	}
}

func TestDockerDeployPassesChannelAndVerifiesBusinessContract(t *testing.T) {
	t.Parallel()

	stateRoot := t.TempDir()
	projectStateDir := filepath.Join(stateRoot, "dws")
	if err := os.MkdirAll(projectStateDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(project state) error = %v", err)
	}
	previousImage := immutableImage("b")
	candidateImage := immutableImage("c")
	stateFile := filepath.Join(projectStateDir, "image.env")
	mustWriteFile(t, stateFile, []byte("IMAGE_REF="+previousImage+"\n"), 0o600)

	deployLog := filepath.Join(t.TempDir(), "deploy.log")
	fakeDeploy := writeFakeDeployScript(t, stateFile, deployLog)
	server := newDWSHTTPServer(t, func() string { return readCurrentImage(t, stateFile) }, "")
	tokenFile := writeTokenFile(t)

	cmd := dockerDeployCommand(t, fakeDeploy, stateRoot, server.URL, tokenFile, candidateImage)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("deploy-dws-docker.sh error = %v\noutput:\n%s", err, output)
	}

	deployCalls := readLines(t, deployLog)
	if len(deployCalls) != 1 {
		t.Fatalf("deploy calls = %v, want one candidate deployment", deployCalls)
	}
	if want := "test-registered-channel|" + candidateImage; deployCalls[0] != want {
		t.Fatalf("deploy call = %q, want %q", deployCalls[0], want)
	}
	if got := readCurrentImage(t, stateFile); got != candidateImage {
		t.Fatalf("current image = %q, want %q", got, candidateImage)
	}
	if !strings.Contains(string(output), "DWS_DEPLOYED_IMAGE_REF="+candidateImage) {
		t.Fatalf("deployment output missing immutable image:\n%s", output)
	}
}

func TestDockerDeployRollsBackWhenBusinessContractFails(t *testing.T) {
	t.Parallel()

	stateRoot := t.TempDir()
	projectStateDir := filepath.Join(stateRoot, "dws")
	if err := os.MkdirAll(projectStateDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(project state) error = %v", err)
	}
	previousImage := immutableImage("d")
	candidateImage := immutableImage("e")
	stateFile := filepath.Join(projectStateDir, "image.env")
	mustWriteFile(t, stateFile, []byte("IMAGE_REF="+previousImage+"\n"), 0o600)

	deployLog := filepath.Join(t.TempDir(), "deploy.log")
	fakeDeploy := writeFakeDeployScript(t, stateFile, deployLog)
	server := newDWSHTTPServer(
		t,
		func() string { return readCurrentImage(t, stateFile) },
		candidateImage,
	)
	tokenFile := writeTokenFile(t)

	cmd := dockerDeployCommand(t, fakeDeploy, stateRoot, server.URL, tokenFile, candidateImage)
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("deploy-dws-docker.sh succeeded after failed business verification:\n%s", output)
	}

	deployCalls := readLines(t, deployLog)
	wantCalls := []string{
		"test-registered-channel|" + candidateImage,
		"test-registered-channel|" + previousImage,
	}
	if fmt.Sprint(deployCalls) != fmt.Sprint(wantCalls) {
		t.Fatalf("deploy calls = %v, want %v\noutput:\n%s", deployCalls, wantCalls, output)
	}
	if got := readCurrentImage(t, stateFile); got != previousImage {
		t.Fatalf("current image after rollback = %q, want %q", got, previousImage)
	}
	if !strings.Contains(string(output), "restored previous Docker image") {
		t.Fatalf("deployment output missing rollback evidence:\n%s", output)
	}
}

func TestDockerDeployRejectsDigestFromAnotherRegistry(t *testing.T) {
	t.Parallel()

	stateRoot := t.TempDir()
	projectStateDir := filepath.Join(stateRoot, "dws")
	if err := os.MkdirAll(projectStateDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(project state) error = %v", err)
	}
	stateFile := filepath.Join(projectStateDir, "image.env")
	deployLog := filepath.Join(t.TempDir(), "deploy.log")
	fakeDeploy := writeFakeDeployScript(t, stateFile, deployLog)
	server := newDWSHTTPServer(t, nil, "")
	tokenFile := writeTokenFile(t)
	wrongRegistryImage := "registryXexampleYcom:5443/dws@sha256:" + strings.Repeat("f", 64)

	cmd := dockerDeployCommand(t, fakeDeploy, stateRoot, server.URL, tokenFile, wrongRegistryImage)
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("deploy-dws-docker.sh accepted another registry digest:\n%s", output)
	}
	if _, statErr := os.Stat(deployLog); !os.IsNotExist(statErr) {
		t.Fatalf("deploy script ran for another registry digest; Stat error = %v", statErr)
	}
}

func dockerDeployCommand(
	t *testing.T,
	deployScript string,
	stateRoot string,
	baseURL string,
	tokenFile string,
	imageRef string,
) *exec.Cmd {
	t.Helper()

	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("Abs(repo root) error = %v", err)
	}
	cmd := exec.Command(
		"bash", repoScriptPath(t, "deploy-dws-docker.sh"),
		"--deploy-script", deployScript,
		"--compose-file", filepath.Join(repoRoot, "compose.yaml"),
		"--image-ref", imageRef,
		"--registry-host", "registry.example.com",
		"--channel-code", "test-registered-channel",
		"--profile", testProfile,
		"--token-file", tokenFile,
		"--base-url", baseURL,
		"--verify-group", testGroup,
		"--verify-title", testTitle,
	)
	cmd.Env = append(os.Environ(), "DEPLOY_IMAGE_STATE_DIR="+stateRoot)
	return cmd
}

func newDWSHTTPServer(t *testing.T, currentImage func() string, failedImage string) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); r.URL.Path != "/healthz" && r.URL.Path != "/readyz" && got != "Bearer "+testToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/healthz", "/readyz":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/v1/schema":
			_, _ = w.Write([]byte(`{"data":{"metadata":{"command_count":6}}}`))
		case "/v1/commands/contact.get_current_user_profile/execute":
			_, _ = fmt.Fprintf(w, `{"data":{"content":{"success":true,"result":[{"orgEmployeeModel":{"corpId":%q}}]}}}`, testProfile)
		case "/v1/commands/chat.get_conversation_info/execute":
			if currentImage != nil && failedImage == currentImage() {
				_, _ = w.Write([]byte(`{"data":{"content":{"success":false}}}`))
				return
			}
			_, _ = fmt.Fprintf(w, `{"data":{"content":{"success":true,"result":{"conversationInfo":{"corpId":%q,"title":%q}}}}}`, testProfile, testTitle)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func writeFakeDeployScript(t *testing.T, stateFile string, deployLog string) string {
	t.Helper()

	scriptPath := filepath.Join(t.TempDir(), "deploy-image.sh")
	script := fmt.Sprintf(`#!/usr/bin/env bash
set -Eeuo pipefail
printf '%%s|%%s\n' "${DWS_CHANNEL:-}" "$3" >>%q
printf 'IMAGE_REF=%%s\n' "$3" >%q
`, deployLog, stateFile)
	mustWriteFile(t, scriptPath, []byte(script), 0o755)
	return scriptPath
}

func writeTokenFile(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "http-token")
	mustWriteFile(t, path, []byte(testToken+"\n"), 0o600)
	return path
}

func repoScriptPath(t *testing.T, name string) string {
	t.Helper()

	path, err := filepath.Abs(filepath.Join("..", "..", "scripts", name))
	if err != nil {
		t.Fatalf("Abs(%s) error = %v", name, err)
	}
	return path
}

func immutableImage(fill string) string {
	return "registry.example.com:5443/dws@sha256:" + strings.Repeat(fill, 64)
}

func readCurrentImage(t *testing.T, stateFile string) string {
	t.Helper()

	data, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", stateFile, err)
	}
	line := strings.TrimSpace(string(data))
	return strings.TrimPrefix(line, "IMAGE_REF=")
}

func readLines(t *testing.T, path string) []string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", path, err)
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}
