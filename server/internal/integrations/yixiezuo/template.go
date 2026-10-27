package yixiezuo

import "strings"

// DefaultRequirementTemplateURL is the 易协作 issue used as a form template
// when a requirement draft does not name one. A robot's saved
// requirement_template_url, or a URL in the /requirement command, replaces it.
// Ordinary POPO messages still require a saved robot template so they do not
// take over /issue and normal chat.
const DefaultRequirementTemplateURL = "https://dj01.pm.netease.com/issues/108402"

// ResolveRequirementTemplateURL prefers an explicit command URL, then a saved
// robot template, then the built-in default.
func ResolveRequirementTemplateURL(configured, override string) string {
	if override = strings.TrimSpace(override); override != "" {
		return override
	}
	if configured = strings.TrimSpace(configured); configured != "" {
		return configured
	}
	return DefaultRequirementTemplateURL
}
