package execenv

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/multica-ai/multica/server/pkg/agent"
)

func privateSkillHomeVariable(provider string) string {
	switch provider {
	case "kimi":
		return "KIMI_CODE_HOME"
	case "qwen":
		return "QWEN_HOME"
	case "kiro":
		return "KIRO_HOME"
	case "reasonix":
		return "REASONIX_HOME"
	case "mcode":
		return "MINIMAX_DATA_DIR"
	default:
		return ""
	}
}

// PreparePrivateSkillHome uses the runtime's own home override. Root config
// files are copied, existing auth and session entries retain their links,
// and only the skills directory is composed from task and user entries.
func PreparePrivateSkillHome(provider, privateRuntimeDir, workDir, version string, skillCount int, taskEnv map[string]string) (map[string]string, error) {
	key := privateSkillHomeVariable(provider)
	if !agent.SupportsPrivateSkillHome(provider, version) || privateRuntimeDir == "" || skillCount == 0 {
		return nil, nil
	}
	if _, err := PrivateSkillDirs(privateRuntimeDir); err != nil {
		return nil, err
	}
	effective := reasonixEnv(taskEnv)
	source := strings.TrimSpace(effective.lookup(key))
	if provider == "mcode" && source == "" {
		source = strings.TrimSpace(effective.lookup("MAVIS_DATA_DIR"))
	}
	if source != "" {
		source = effective.expandVars(source)
		if source == "~" {
			source = effective.userHomeDir()
		} else if strings.HasPrefix(source, "~/") || strings.HasPrefix(source, `~\`) {
			source = filepath.Join(effective.userHomeDir(), source[2:])
		} else if !filepath.IsAbs(source) {
			source = filepath.Join(workDir, source)
		}
	}
	if source == "" {
		if provider == "reasonix" {
			source = effective.homeDir()
		} else {
			folder := "." + provider
			if provider == "kimi" {
				folder = ".kimi-code"
			} else if provider == "mcode" {
				folder = ".minimax"
			}
			source = filepath.Join(effective.userHomeDir(), folder)
		}
	}
	if !filepath.IsAbs(source) {
		return nil, fmt.Errorf("%s skill home must be absolute", provider)
	}
	if rel, err := filepath.Rel(source, privateRuntimeDir); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("%s skill home contains the task's private directory", provider)
	}
	target := filepath.Join(privateRuntimeDir, provider+"-home")
	if err := os.Mkdir(target, 0o700); err != nil {
		return nil, fmt.Errorf("create %s private skill home: %w", provider, err)
	}
	// These are native session stores, not managed skill/config writes.
	// Establish their links even before the first session exists; otherwise
	// the runtime would create a task-local store that disappears on resume.
	var sessionDirs []string
	switch provider {
	case "kimi", "kiro":
		sessionDirs = []string{"sessions"}
	case "qwen":
		sessionDirs = []string{"tmp", "projects"}
	}
	for _, dir := range sessionDirs {
		if err := os.MkdirAll(filepath.Join(source, dir), 0o700); err != nil {
			return nil, fmt.Errorf("prepare %s session store: %w", provider, err)
		}
	}
	if err := mirrorPrivateSkillHome(source, target); err != nil {
		return nil, fmt.Errorf("prepare %s private skill home: %w", provider, err)
	}
	if provider == "reasonix" {
		// Reasonix may still read an older config location when its primary
		// config is absent. Preserve that selection before isolating its home.
		configPath := effective.userConfigLoadPath()
		if configPath != "" && !reasonixSamePath(configPath, filepath.Join(source, "config.toml")) {
			if err := copyFile(configPath, filepath.Join(target, "config.toml")); err != nil {
				return nil, fmt.Errorf("preserve reasonix config: %w", err)
			}
			if err := os.Chmod(filepath.Join(target, "config.toml"), 0o600); err != nil {
				return nil, err
			}
		}
	}
	if err := mergePrivateSkillHomeEntries(filepath.Join(privateRuntimeDir, "skills"), filepath.Join(source, "skills"), filepath.Join(target, "skills"), provider == "mcode"); err != nil {
		return nil, fmt.Errorf("prepare %s native skills: %w", provider, err)
	}
	result := map[string]string{key: target}
	if reasonixGOOS == "windows" {
		for existing := range taskEnv {
			if strings.EqualFold(existing, key) {
				result[existing] = target
			}
		}
	}
	return result, nil
}

func mirrorPrivateSkillHome(source, target string) error {
	entries, err := os.ReadDir(source)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() == "skills" {
			continue
		}
		src, dst := filepath.Join(source, entry.Name()), filepath.Join(target, entry.Name())
		switch entry.Name() {
		case "config.toml", "config.yaml", "settings.json", "mcp.json":
			if err := copyFile(src, dst); err != nil {
				return err
			}
			if err := os.Chmod(dst, 0o600); err != nil {
				return err
			}
		default:
			info, err := os.Stat(src)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return err
			}
			if info.IsDir() {
				err = createDirLink(src, dst)
			} else {
				// Auth refreshes and session writes must reach the original
				// state. A copied file would silently lose both on the next task.
				err = os.Symlink(src, dst)
				if err != nil {
					// A same-volume hard link shares file writes on Windows
					// without requiring Developer Mode. Never copy mutable state.
					err = os.Link(src, dst)
				}
			}
			if err != nil {
				return fmt.Errorf("link runtime state %s (enable file symlinks or keep the task and runtime homes on one volume): %w", entry.Name(), err)
			}
		}
	}
	return nil
}

func mergePrivateSkillHomeEntries(managed, user, target string, copyUser bool) error {
	if err := os.Mkdir(target, 0o700); err != nil {
		return err
	}
	// Managed entries win a same-directory collision. Other user skills
	// remain inputs, so preparing this home never writes through them.
	for _, root := range []string{managed, user} {
		if root == user && root == "" {
			continue
		}
		entries, err := os.ReadDir(root)
		if os.IsNotExist(err) && root == user {
			continue
		}
		if err != nil {
			return err
		}
		for _, entry := range entries {
			dst := filepath.Join(target, entry.Name())
			if _, err := os.Lstat(dst); err == nil {
				continue
			} else if !os.IsNotExist(err) {
				return err
			}
			src := filepath.Join(root, entry.Name())
			// Keep managed files beneath the native root itself. Some
			// runtimes reject directory links that escape their skill root.
			if root == managed || copyUser {
				info, err := os.Stat(src)
				if err != nil {
					return err
				}
				if !info.IsDir() {
					if err := copyFile(src, dst); err != nil {
						return err
					}
					continue
				}
				if err := os.CopyFS(dst, os.DirFS(src)); err != nil {
					return err
				}
				continue
			}
			if err := linkSharedHermesEntry(src, dst); err != nil {
				return err
			}
		}
	}
	return nil
}
