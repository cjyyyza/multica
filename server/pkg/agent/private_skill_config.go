package agent

// SupportsPrivateSkillConfig follows the execution backend's version selection.
// OpenCode 2 ignores OPENCODE_CONFIG_DIR as well as the inline-config channel.
func SupportsPrivateSkillConfig(provider, version string, builtin bool) bool {
	switch provider {
	case "opencode":
		return !opencodeUsesV2Contract(Config{CLIVersion: version, BuiltinRuntime: builtin})
	case "deveco":
		return true
	default:
		return false
	}
}

// SupportsPrivateSkillHome gates home overrides whose introduction is known.
func SupportsPrivateSkillHome(provider, version string) bool {
	switch provider {
	case "kiro":
		v, err := parseSemver(version)
		return err == nil && !v.lessThan(semver{Major: 2, Minor: 3})
	case "kimi", "qwen", "reasonix", "mcode":
		return true
	default:
		return false
	}
}
