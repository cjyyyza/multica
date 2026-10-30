package execenv

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// dshSkillPatchFile is a per-task `dsh --patch` overlay. dsh catalogs only
// the skill roots it scans; a SKILL.md outside those roots is an ordinary
// file. P4 tasks keep managed skills out of the Helix client, so this
// overlay adds that private directory as skill-filesystem customSkillDirs.
//
// A dsh patch replaces the targeted row's whole config. The base
// skill-filesystem row has none, and this overlay therefore restates
// includeDefaultRoots so project and user roots stay scanned. A profile
// cordis.patch.yml that itself sets skill-filesystem config is replaced
// for this process only.
const dshSkillPatchFile = "dsh-skill-roots.patch.yml"

type dshSkillPatchEntry struct {
	ID     string              `yaml:"id"`
	Config dshSkillPatchConfig `yaml:"config"`
}

type dshSkillPatchConfig struct {
	IncludeDefaultRoots bool     `yaml:"includeDefaultRoots"`
	CustomSkillDirs     []string `yaml:"customSkillDirs"`
}

// DshSkillPatchPath is the overlay written beside a P4 task's private skills.
// An empty private directory has no overlay.
func DshSkillPatchPath(privateRuntimeDir string) string {
	if privateRuntimeDir == "" {
		return ""
	}
	return filepath.Join(privateRuntimeDir, dshSkillPatchFile)
}

// DshConfigPatchForTask returns the overlay path dsh must receive before
// --stdio, or empty when this task does not need one. A dsh task with
// managed skills and a private runtime directory fails when the overlay is
// missing: launching without it drops those skills from the catalog.
func DshConfigPatchForTask(provider, privateRuntimeDir string, skillCount int) (string, error) {
	if provider != "dsh" || privateRuntimeDir == "" || skillCount == 0 {
		return "", nil
	}
	patch := DshSkillPatchPath(privateRuntimeDir)
	if _, err := os.Stat(patch); err != nil {
		return "", fmt.Errorf("dsh private skill patch: %w", err)
	}
	return patch, nil
}

func writeDshPrivateSkillPatch(privateDir string, manifest *sidecarManifest) error {
	skillsDir := filepath.Join(privateDir, "skills")
	data, err := yaml.Marshal([]dshSkillPatchEntry{{
		ID: "skill-filesystem",
		Config: dshSkillPatchConfig{
			IncludeDefaultRoots: true,
			CustomSkillDirs:     []string{filepath.ToSlash(skillsDir)},
		},
	}})
	if err != nil {
		return fmt.Errorf("marshal dsh skill patch: %w", err)
	}
	if err := recordWriteFile(DshSkillPatchPath(privateDir), data, 0o600, manifest); err != nil {
		return fmt.Errorf("write dsh skill patch: %w", err)
	}
	return nil
}
