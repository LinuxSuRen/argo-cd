package helm

import (
	"fmt"
	"io/ioutil"
	"net/url"
	"os"
	"os/exec"
	"path"
	"strings"

	"github.com/ghodss/yaml"

	"github.com/argoproj/argo-cd/v2/util/config"
	executil "github.com/argoproj/argo-cd/v2/util/exec"
)

type HelmRepository struct {
	Creds
	Name      string
	Repo      string
	EnableOci bool
}

// Helm provides wrapper functionality around the `helm` command.
type Helm interface {
	// Template returns a list of unstructured objects from a `helm template` command
	Template(opts *TemplateOpts) (string, error)
	// GetParameters returns a list of chart parameters taking into account values in provided YAML files.
	GetParameters(valuesFiles []string) (map[string]string, error)
	// DependencyBuild runs `helm dependency build` to download a chart's dependencies
	DependencyBuild() error
	// Init runs `helm init --client-only`
	Init() error
	// Dispose deletes temp resources
	Dispose()
}

// NewHelmApp create a new wrapper to run commands on the `helm` command-line tool.
func NewHelmApp(workDir string, repos []HelmRepository, isLocal bool, version string, proxy string) (Helm, error) {
	cmd, err := NewCmd(workDir, version, proxy)
	if err != nil {
		return nil, err
	}
	cmd.IsLocal = isLocal

	return &helm{repos: repos, cmd: *cmd}, nil
}

type helm struct {
	cmd   Cmd
	repos []HelmRepository
}

var _ Helm = &helm{}

// IsMissingDependencyErr tests if the error is related to a missing chart dependency
func IsMissingDependencyErr(err error) bool {
	return strings.Contains(err.Error(), "found in requirements.yaml, but missing in charts") ||
		strings.Contains(err.Error(), "found in Chart.yaml, but missing in charts/ directory")
}

func (h *helm) Template(templateOpts *TemplateOpts) (string, error) {
	out, err := h.cmd.template(".", templateOpts)
	if err != nil {
		return "", err
	}
	return out, nil
}

func (h *helm) DependencyBuild() error {
	isHelmOci := h.cmd.IsHelmOci
	defer func() {
		h.cmd.IsHelmOci = isHelmOci
	}()

	for i := range h.repos {
		repo := h.repos[i]
		if repo.EnableOci {
			h.cmd.IsHelmOci = true
			if repo.Creds.Username != "" && repo.Creds.Password != "" {
				_, err := h.cmd.Login(repo.Repo, repo.Creds)

				defer func() {
					_, _ = h.cmd.Logout(repo.Repo, repo.Creds)
				}()

				if err != nil {
					return err
				}
			}
		} else {
			_, err := h.cmd.RepoAdd(repo.Name, repo.Repo, repo.Creds)

			if err != nil {
				return err
			}
		}
	}
	h.repos = nil
	_, err := h.cmd.dependencyBuild()
	return err
}

func (h *helm) Init() error {
	_, err := h.cmd.Init()
	return err
}

func (h *helm) Dispose() {
	h.cmd.Close()
}

func Version(shortForm bool) (string, error) {
	executable := "helm"
	cmdArgs := []string{"version", "--client"}
	if shortForm {
		cmdArgs = append(cmdArgs, "--short")
	}
	cmd := exec.Command(executable, cmdArgs...)
	// example version output:
	// long: "version.BuildInfo{Version:\"v3.3.1\", GitCommit:\"249e5215cde0c3fa72e27eb7a30e8d55c9696144\", GitTreeState:\"clean\", GoVersion:\"go1.14.7\"}"
	// short: "v3.3.1+g249e521"
	version, err := executil.RunWithRedactor(cmd, redactor)
	if err != nil {
		return "", fmt.Errorf("could not get helm version: %s", err)
	}
	return strings.TrimSpace(version), nil
}

func (h *helm) GetParameters(valuesFiles []string) (map[string]string, error) {
	out, err := h.cmd.inspectValues(".")
	if err != nil {
		return nil, err
	}
	values := []string{out}
	for _, file := range valuesFiles {
		var fileValues []byte
		parsedURL, err := url.ParseRequestURI(file)
		if err == nil && (parsedURL.Scheme == "http" || parsedURL.Scheme == "https") {
			fileValues, err = config.ReadRemoteFile(file)
		} else {
			filePath := path.Join(h.cmd.WorkDir, file)
			if _, err := os.Stat(filePath); os.IsNotExist(err) {
				continue
			}
			fileValues, err = ioutil.ReadFile(filePath)
		}
		if err != nil {
			return nil, fmt.Errorf("failed to read value file %s: %s", file, err)
		}
		values = append(values, string(fileValues))
	}

	output := map[string]string{}
	for _, file := range values {
		values := map[string]interface{}{}
		if err = yaml.Unmarshal([]byte(file), &values); err != nil {
			return nil, fmt.Errorf("failed to parse values: %s", err)
		}
		flatVals(values, output)
	}

	return output, nil
}

func flatVals(input interface{}, output map[string]string, prefixes ...string) {
	switch i := input.(type) {
	case map[string]interface{}:
		for k, v := range i {
			flatVals(v, output, append(prefixes, k)...)
		}
	case []interface{}:
		p := append([]string(nil), prefixes...)
		for j, v := range i {
			flatVals(v, output, append(p[0:len(p)-1], fmt.Sprintf("%s[%v]", prefixes[len(p)-1], j))...)
		}
	default:
		output[strings.Join(prefixes, ".")] = fmt.Sprintf("%v", i)
	}
}
