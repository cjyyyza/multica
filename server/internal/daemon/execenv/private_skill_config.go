package execenv

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/multica-ai/multica/server/pkg/agent"
)

// PreparePrivateSkillConfigDir adds a native config directory instead of
// setting skills.paths: an inline array would replace paths from config files.
// Global/project config, inline config, auth and session locations stay native.
func PreparePrivateSkillConfigDir(provider, privateDir, workDir, version string, builtin bool, count int, taskEnv map[string]string) (map[string]string, error) {
	if privateDir == "" || count == 0 || !agent.SupportsPrivateSkillConfig(provider, version, builtin) {
		return nil, nil
	}
	if _, err := PrivateSkillDirs(privateDir); err != nil {
		return nil, err
	}
	key := "OPENCODE_CONFIG_DIR"
	if provider == "deveco" {
		key = "DEVECO_CONFIG_DIR"
	}
	effective := reasonixEnv(taskEnv)
	source := strings.TrimSpace(effective.lookup(key))
	if source != "" {
		if source == "~" {
			source = effective.userHomeDir()
		} else if strings.HasPrefix(source, "~/") || strings.HasPrefix(source, `~\`) {
			source = filepath.Join(effective.userHomeDir(), source[2:])
		} else if !filepath.IsAbs(source) {
			source = filepath.Join(workDir, source)
		}
		if rel, err := filepath.Rel(source, privateDir); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("%s config directory contains task scratch", provider)
		}
	}
	target := filepath.Join(privateDir, provider+"-config")
	if err := os.Mkdir(target, 0o700); err != nil {
		return nil, err
	}
	if source != "" {
		entries, err := os.ReadDir(source)
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		for _, entry := range entries {
			if entry.Name() == "skills" {
				continue
			}
			src, dst := filepath.Join(source, entry.Name()), filepath.Join(target, entry.Name())
			info, err := os.Stat(src)
			if err != nil {
				return nil, err
			}
			if info.IsDir() {
				err = createDirLink(src, dst)
			} else {
				err = copyFile(src, dst)
			}
			if err != nil {
				return nil, fmt.Errorf("preserve %s config input %s: %w", provider, entry.Name(), err)
			}
		}
	}
	userSkills := ""
	if source != "" {
		userSkills = filepath.Join(source, "skills")
	}
	if err := mergePrivateSkillHomeEntries(filepath.Join(privateDir, "skills"), userSkills, filepath.Join(target, "skills"), false); err != nil {
		return nil, err
	}
	result := map[string]string{key: target}
	if reasonixGOOS == "windows" {
		for name := range taskEnv {
			if strings.EqualFold(name, key) {
				result[name] = target
			}
		}
	}
	return result, nil
}
