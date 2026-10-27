package text

import "strings"

// requirementOpeningCopy is the first visible message in a requirement chat.
// It carries the source requirement's title and hands the turn to the agent,
// which clarifies the requirement until there is an approvable candidate.
//
// %[1]s is the title from the source snapshot. It is authored outside Multica,
// so Render escapes it like any other interpolated name.
var requirementOpeningCopy = OpeningCopy{
	LocaleEn: `Requirement: %[1]s

Please continue clarifying the goal, non-goals, scope, constraints, and acceptance criteria so we can form an approvable candidate.`,

	LocaleZhHans: `需求：%[1]s

请继续澄清目标、非目标、范围、约束和验收标准，形成可审批的候选。`,

	LocaleJa: `要件：%[1]s

目標、非目標、スコープ、制約、受け入れ条件を引き続き明確にして、承認できる候補にまとめてください。`,

	LocaleKo: `요구사항: %[1]s

목표, 비목표, 범위, 제약, 수락 기준을 계속 명확히 하여 승인 가능한 후보로 정리해 주세요.`,

	LocaleFr: `Exigence : %[1]s

Continuez à clarifier l’objectif, les non-objectifs, le périmètre, les contraintes et les critères d’acceptation afin de produire un candidat approuvable.`,
}

var requirementOpeningTitleLineBreaks = strings.NewReplacer(
	"\r\n", " ",
	"\r", " ",
	"\n", " ",
)

// BuildRequirementOpening renders the product-authored opening for a
// requirement chat in the member's language. Source line breaks become spaces
// so title text cannot start Markdown blocks; Render escapes inline Markdown.
func BuildRequirementOpening(locale Locale, title string) string {
	title = requirementOpeningTitleLineBreaks.Replace(title)
	return requirementOpeningCopy.Render(locale, title)
}
