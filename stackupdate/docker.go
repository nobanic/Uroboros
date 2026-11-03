package stackupdate

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type containerInfo struct {
	ID     string `json:"Id"`
	Image  string
	Config struct {
		Labels map[string]string
		User   string
	}
	HostConfig struct{ GroupAdd []string }
	State      struct {
		Running  bool
		Status   string
		ExitCode int
	}
	Mounts []struct{ Type, Name, Source, Destination string }
}

func (c *Controller) inspect(ctx context.Context, reference string) (containerInfo, error) {
	var rows []containerInfo
	b, err := c.command(ctx, "docker", []string{"inspect", reference}, commandOptions{})
	if err != nil {
		return containerInfo{}, err
	}
	if json.Unmarshal(b, &rows) != nil || len(rows) != 1 {
		return containerInfo{}, errors.New("invalid Docker container metadata")
	}
	return rows[0], nil
}

func (c *Controller) self(ctx context.Context) (containerInfo, string, string, error) {
	info, err := c.inspect(ctx, os.Getenv("HOSTNAME"))
	if err != nil {
		return info, "", "", err
	}
	if info.Config.Labels["com.docker.compose.project"] != c.Config.Project || info.Config.Labels["io.uroboros.updater"] != "1" {
		return info, "", "", errors.New("updater does not belong to this installation")
	}
	var hostState, hostInstall string
	for _, mount := range info.Mounts {
		if mount.Type != "bind" {
			continue
		}
		switch mount.Destination {
		case stateRoot:
			hostState = mount.Source
		case installRoot:
			hostInstall = mount.Source
		}
	}
	hostState, hostInstall = daemonPath(hostState), daemonPath(hostInstall)
	if !strings.HasPrefix(hostState, "/") || !strings.HasPrefix(hostInstall, "/") {
		return info, "", "", errors.New("private updater bind mounts are unavailable")
	}
	return info, hostState, hostInstall, nil
}

func daemonPath(source string) string {
	// Docker Desktop reports the original drive path even to a Linux client.
	// Its daemon bind namespace exposes that drive through this verified mount.
	if len(source) >= 3 && source[1] == ':' && ((source[0] >= 'A' && source[0] <= 'Z') || (source[0] >= 'a' && source[0] <= 'z')) {
		return "/run/desktop/mnt/host/" + strings.ToLower(source[:1]) + strings.ReplaceAll(source[2:], `\`, "/")
	}
	return source
}

func bindArgument(source, destination string, readonly bool) string {
	var encoded strings.Builder
	writer := csv.NewWriter(&encoded)
	fields := []string{"type=bind", "src=" + source, "dst=" + destination}
	if readonly {
		fields = append(fields, "readonly")
	}
	_ = writer.Write(fields)
	writer.Flush()
	return strings.TrimSuffix(encoded.String(), "\n")
}

func (c *Controller) runtimeEnvironment() []string {
	return []string{"UROBOROS_UPDATE_PROJECT=" + c.Config.Project, "COMPOSE_PROJECT_NAME=" + c.Config.Project,
		"UROBOROS_UPDATER_USER=" + os.Getenv("UROBOROS_UPDATER_USER"), "UROBOROS_DOCKER_GID=" + os.Getenv("UROBOROS_DOCKER_GID"),
		"UROBOROS_UPDATE_TOKEN=" + c.Config.Token, "UROBOROS_SOURCE_REVISION=" + c.Config.Revision,
		"UROBOROS_UPDATE_SEED=" + c.Config.Seed, "UROBOROS_PORT=" + c.Config.Port,
		"UROBOROS_UPDATE_STATE_DIR=" + c.root, "UROBOROS_UPDATE_INSTALL_DIR=" + installRoot}
}

func (c *Controller) launch(ctx context.Context, job string) error {
	self, hostState, hostInstall, err := c.self(ctx)
	if err != nil {
		return err
	}
	args := []string{"create", "--rm", "--name", job, "--label", "com.docker.compose.project=" + c.Config.Project,
		"--label", "io.uroboros.updater=1", "--label", "io.uroboros.update-worker=1", "--network", c.Config.Project + "_update-control",
		"--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--memory", "512m", "--pids-limit", "256",
		"--tmpfs", "/tmp:rw,nosuid,nodev,size=256m", "--mount", bindArgument(hostState, "/state", false),
		"--mount", bindArgument(hostInstall, "/install", true), "--mount", bindArgument("/var/run/docker.sock", "/var/run/docker.sock", false)}
	if self.Config.User != "" {
		args = append(args, "--user", self.Config.User)
	}
	for _, group := range self.HostConfig.GroupAdd {
		args = append(args, "--group-add", group)
	}
	for _, key := range []string{"POSTGRES_PASSWORD", "ARSENAL_TOKEN", "API_LAB_TOKEN", "UROBOROS_UPDATE_TOKEN", "UROBOROS_UPDATE_PROJECT", "UROBOROS_SOURCE_REVISION", "UROBOROS_UPDATE_SEED", "UROBOROS_PORT", "UROBOROS_UPDATER_USER", "UROBOROS_DOCKER_GID", "UROBOROS_UPDATE_GITHUB_TOKEN"} {
		args = append(args, "--env", key)
	}
	args = append(args, self.Image, "run")
	opts := commandOptions{Env: c.runtimeEnvironment()}
	if _, err := c.command(ctx, "docker", args, opts); err != nil {
		return err
	}
	if _, err := c.command(ctx, "docker", []string{"network", "connect", c.Config.Project + "_egress", job}, commandOptions{}); err != nil {
		_, _ = c.command(ctx, "docker", []string{"rm", job}, commandOptions{})
		return err
	}
	_, err = c.command(ctx, "docker", []string{"start", job}, commandOptions{})
	return err
}

type model map[string]any

func object(value any) map[string]any { result, _ := value.(map[string]any); return result }
func textValue(value any) string      { text, _ := value.(string); return text }

func mapBind(source, root, hostRoot, hostState, hostInstall string) (string, error) {
	for _, pair := range [][2]string{{root, hostRoot}, {stateRoot, hostState}, {installRoot, hostInstall}} {
		if source == pair[0] {
			return pair[1], nil
		}
		if strings.HasPrefix(source, pair[0]+"/") {
			return pair[1] + strings.TrimPrefix(source, pair[0]), nil
		}
	}
	if source == "/var/run/docker.sock" {
		return source, nil
	}
	return "", fmt.Errorf("source declares a bind mount outside its managed directories: %s", source)
}

func (c *Controller) render(ctx context.Context, root, revision, hostRoot, hostState, hostInstall string) (model, error) {
	args := []string{"compose", "--project-name", c.Config.Project, "--project-directory", filepath.Join(root, "clean"), "-f", filepath.Join(root, "clean", "compose.yml"), "config", "--format", "json"}
	env := append(c.runtimeEnvironment(), "UROBOROS_SOURCE_REVISION="+revision)
	b, err := c.command(ctx, "docker", args, commandOptions{Env: env})
	if err != nil {
		return nil, err
	}
	var result model
	if err := json.Unmarshal(b, &result); err != nil {
		return nil, errors.New("invalid clean Compose configuration")
	}
	services := object(result["services"])
	if services["uroboros"] == nil || services["postgres"] == nil || services["updater"] == nil {
		return nil, errors.New("source does not provide the managed clean-stack update protocol")
	}
	for name, raw := range services {
		service := object(raw)
		if build := object(service["build"]); build != nil {
			service["image"] = "uroboros/" + c.Config.Project + "-" + name + ":" + revision
		}
		mounts, _ := service["volumes"].([]any)
		for _, rawMount := range mounts {
			mount := object(rawMount)
			if mount["type"] != "bind" {
				continue
			}
			if textValue(mount["source"]) == "/var/run/docker.sock" && name != "updater" {
				return nil, errors.New("only the isolated updater may mount the Docker socket")
			}
			mapped, err := mapBind(textValue(mount["source"]), root, hostRoot, hostState, hostInstall)
			if err != nil {
				return nil, err
			}
			mount["source"] = mapped
		}
		if name == "uroboros" || name == "updater" {
			environment := object(service["environment"])
			if environment == nil {
				environment = map[string]any{}
				service["environment"] = environment
			}
			environment["UROBOROS_SOURCE_REVISION"] = revision
		}
	}
	object(services["bootstrap"])["image"] = object(services["uroboros"])["image"]
	// Existing persistent stores must stay at their established names. A new
	// release cannot silently select another database, project or port.
	for name, raw := range object(result["volumes"]) {
		if textValue(object(raw)["name"]) != c.Config.Project+"_"+name {
			return nil, errors.New("source changes persistent volume ownership")
		}
	}
	return result, nil
}

func (c *Controller) compose(ctx context.Context, path string, args ...string) ([]byte, error) {
	base := []string{"compose", "--project-name", c.Config.Project, "-f", path}
	return c.command(ctx, "docker", append(base, args...), commandOptions{})
}

func serviceNames(m model, includeInit bool) []string {
	var result []string
	for name := range object(m["services"]) {
		if !includeInit && (name == "bootstrap" || name == "volume-init") {
			continue
		}
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

func (c *Controller) capture(ctx context.Context, previous model) (map[string]containerInfo, error) {
	b, err := c.command(ctx, "docker", []string{"ps", "-a", "--filter", "label=com.docker.compose.project=" + c.Config.Project, "--format", "{{.ID}}"}, commandOptions{})
	if err != nil {
		return nil, err
	}
	result := map[string]containerInfo{}
	for _, id := range strings.Fields(string(b)) {
		info, err := c.inspect(ctx, id)
		if err != nil {
			return nil, err
		}
		if info.Config.Labels["com.docker.compose.project"] != c.Config.Project {
			return nil, errors.New("Docker project ownership changed")
		}
		service := info.Config.Labels["com.docker.compose.service"]
		if service == "" {
			continue
		}
		if object(previous["services"])[service] == nil {
			return nil, errors.New("this project has an unexpected service; refusing an incomplete update")
		}
		result[service] = info
		object(object(previous["services"])[service])["image"] = info.Image
	}
	for _, name := range serviceNames(previous, false) {
		if name == "updater" {
			continue
		}
		if _, ok := result[name]; !ok {
			return nil, fmt.Errorf("service %s is missing; start the complete installation first", name)
		}
	}
	return result, nil
}

func ensureCompatible(previous, candidate model) error {
	for name, raw := range object(previous["volumes"]) {
		if textValue(object(raw)["name"]) != textValue(object(object(candidate["volumes"])[name])["name"]) {
			return fmt.Errorf("source removes or renames persistent store %s", name)
		}
	}
	for _, name := range []string{"postgres", "uroboros", "updater", "arsenal", "api-lab", "binary-broker", "binary-worker", "tor-research", "bootstrap", "volume-init"} {
		if object(candidate["services"])[name] == nil {
			return fmt.Errorf("source removes required service %s", name)
		}
	}
	oldPorts, _ := json.Marshal(object(object(previous["services"])["uroboros"])["ports"])
	newPorts, _ := json.Marshal(object(object(candidate["services"])["uroboros"])["ports"])
	if string(oldPorts) != string(newPorts) {
		return errors.New("source changes the operator console binding")
	}
	return nil
}
